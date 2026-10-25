package repositories

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/domain/repositories"
)

// upsertChunk keeps a statement well under Postgres's 65,535 parameters.
const upsertChunk = 1_000

type postgresBlockStore struct {
	db *sqlx.DB
}

// NewPostgresBlockStore stores blocks in Postgres.
func NewPostgresBlockStore(db *sqlx.DB) repositories.BlockStore {
	return &postgresBlockStore{db: db}
}

type blockRow struct {
	Network      string    `db:"network"`
	Number       int64     `db:"number"`
	BaseFee      numeric   `db:"base_fee"`
	GasUsedRatio float64   `db:"gas_used_ratio"`
	RequiredTip  numeric   `db:"required_tip"`
	MedianTip    numeric   `db:"median_tip"`
	ObservedAt   time.Time `db:"observed_at"`
}

func (r blockRow) toEntity() entities.BlockFee {
	return entities.BlockFee{
		Number:       uint64(r.Number),
		BaseFee:      r.BaseFee.Int,
		GasUsedRatio: r.GasUsedRatio,
		RequiredTip:  r.RequiredTip.Int,
		MedianTip:    r.MedianTip.Int,
		ObservedAt:   r.ObservedAt,
	}
}

func (s *postgresBlockStore) Upsert(ctx context.Context, network string, blocks []entities.BlockFee) error {
	if len(blocks) == 0 {
		return nil
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for start := 0; start < len(blocks); start += upsertChunk {
		chunk := blocks[start:min(start+upsertChunk, len(blocks))]
		values := make([]string, 0, len(chunk))
		args := make([]any, 0, 7*len(chunk))
		for i, b := range chunk {
			p := 7 * i
			values = append(values, fmt.Sprintf("($%d, $%d, $%d, $%d, $%d, $%d, $%d)", p+1, p+2, p+3, p+4, p+5, p+6, p+7))
			args = append(args, network, int64(b.Number), numeric{b.BaseFee}, b.GasUsedRatio,
				numeric{b.RequiredTip}, numeric{b.MedianTip}, b.ObservedAt)
		}
		query := `INSERT INTO blocks (network, number, base_fee, gas_used_ratio, required_tip, median_tip, observed_at)
			VALUES ` + strings.Join(values, ", ") + `
			ON CONFLICT (network, number) DO UPDATE SET
				base_fee = EXCLUDED.base_fee, gas_used_ratio = EXCLUDED.gas_used_ratio,
				required_tip = EXCLUDED.required_tip, median_tip = EXCLUDED.median_tip,
				observed_at = EXCLUDED.observed_at`
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("upsert blocks: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit blocks: %w", err)
	}
	return nil
}

func (s *postgresBlockStore) Range(ctx context.Context, network string, from, to uint64) ([]entities.BlockFee, error) {
	var rows []blockRow
	err := s.db.SelectContext(ctx, &rows, `SELECT * FROM blocks
		WHERE network = $1 AND number BETWEEN $2 AND $3 ORDER BY number`, network, int64(from), int64(to))
	if err != nil {
		return nil, fmt.Errorf("select blocks: %w", err)
	}
	out := make([]entities.BlockFee, len(rows))
	for i, r := range rows {
		out[i] = r.toEntity()
	}
	return out, nil
}

func (s *postgresBlockStore) Latest(ctx context.Context, network string) (uint64, bool, error) {
	var latest sql.NullInt64
	if err := s.db.GetContext(ctx, &latest, `SELECT max(number) FROM blocks WHERE network = $1`, network); err != nil {
		return 0, false, fmt.Errorf("select latest block: %w", err)
	}
	return uint64(latest.Int64), latest.Valid, nil
}

// AverageBlockTime measures chain progress since the oldest stored poll. Blocks backfilled by that poll share its
// observation time, so counting them would make blocks look far faster than they are.
func (s *postgresBlockStore) AverageBlockTime(ctx context.Context, network string) (time.Duration, error) {
	var r struct {
		FirstNumber sql.NullInt64   `db:"first_number"`
		LastNumber  sql.NullInt64   `db:"last_number"`
		Seconds     sql.NullFloat64 `db:"seconds"`
	}
	err := s.db.GetContext(ctx, &r, `
		WITH first_poll AS (SELECT min(observed_at) AS at FROM blocks WHERE network = $1)
		SELECT
			(SELECT max(number) FROM blocks, first_poll WHERE network = $1 AND observed_at = first_poll.at) AS first_number,
			max(number) AS last_number,
			EXTRACT(EPOCH FROM max(observed_at) - (SELECT at FROM first_poll))::float8 AS seconds
		FROM blocks WHERE network = $1`, network)
	if err != nil {
		return 0, fmt.Errorf("measure block time: %w", err)
	}
	if !r.FirstNumber.Valid || !r.Seconds.Valid || r.Seconds.Float64 <= 0 || r.LastNumber.Int64 <= r.FirstNumber.Int64 {
		return 0, entities.ErrInsufficientHistory
	}
	blocks := float64(r.LastNumber.Int64 - r.FirstNumber.Int64)
	return time.Duration(r.Seconds.Float64 / blocks * float64(time.Second)), nil
}

func (s *postgresBlockStore) Prune(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM blocks WHERE observed_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune blocks: %w", err)
	}
	return res.RowsAffected()
}
