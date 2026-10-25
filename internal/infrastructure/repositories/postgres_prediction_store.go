package repositories

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/domain/repositories"
)

// maxPriceDecimals is the scale of snapshots.native_usd.
const maxPriceDecimals = 18

type postgresSnapshotStore struct {
	db *sqlx.DB
}

// NewPostgresSnapshotStore stores snapshots in Postgres.
func NewPostgresSnapshotStore(db *sqlx.DB) repositories.SnapshotStore {
	return &postgresSnapshotStore{db: db}
}

type snapshotRow struct {
	ID             int64          `db:"id"`
	Network        string         `db:"network"`
	AtBlock        int64          `db:"at_block"`
	ObservedAt     time.Time      `db:"observed_at"`
	NextBaseFee    numeric        `db:"next_base_fee"`
	NodeTip        numeric        `db:"node_tip"`
	NativeUSD      sql.NullString `db:"native_usd"`
	PriceUpdatedAt sql.NullTime   `db:"price_updated_at"`
	L1Fees         []byte         `db:"l1_fees"`
}

func (s *postgresSnapshotStore) Insert(ctx context.Context, snap entities.FeeSnapshot) (int64, error) {
	var (
		usd       sql.NullString
		updatedAt sql.NullTime
	)
	if snap.Price != nil {
		if snap.Price.Decimals > maxPriceDecimals {
			return 0, fmt.Errorf("price has %d decimals, the column holds %d", snap.Price.Decimals, maxPriceDecimals)
		}
		usd = sql.NullString{String: decimalString(snap.Price.Answer, snap.Price.Decimals), Valid: true}
		updatedAt = sql.NullTime{Time: snap.Price.UpdatedAt, Valid: true}
	}
	l1 := make(map[string]string, len(snap.L1Fees))
	for name, fee := range snap.L1Fees {
		l1[name] = fee.String()
	}
	l1JSON, err := json.Marshal(l1)
	if err != nil {
		return 0, fmt.Errorf("encode l1 fees: %w", err)
	}
	var id int64
	err = s.db.GetContext(ctx, &id, `INSERT INTO snapshots
		(network, at_block, observed_at, next_base_fee, node_tip, native_usd, price_updated_at, l1_fees)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		snap.Network, int64(snap.AtBlock), snap.ObservedAt, numeric{snap.NextBaseFee}, numeric{snap.NodeTip},
		usd, updatedAt, l1JSON)
	if err != nil {
		return 0, fmt.Errorf("insert snapshot: %w", err)
	}
	return id, nil
}

func (s *postgresSnapshotStore) Get(ctx context.Context, ids []int64) (map[int64]entities.FeeSnapshot, error) {
	out := make(map[int64]entities.FeeSnapshot, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []snapshotRow
	if err := s.db.SelectContext(ctx, &rows, `SELECT * FROM snapshots WHERE id = ANY($1)`, ids); err != nil {
		return nil, fmt.Errorf("select snapshots: %w", err)
	}
	for _, r := range rows {
		snap, err := r.toEntity()
		if err != nil {
			return nil, fmt.Errorf("snapshot %d: %w", r.ID, err)
		}
		out[r.ID] = snap
	}
	return out, nil
}

func (r snapshotRow) toEntity() (entities.FeeSnapshot, error) {
	snap := entities.FeeSnapshot{
		ID:          r.ID,
		Network:     r.Network,
		AtBlock:     uint64(r.AtBlock),
		ObservedAt:  r.ObservedAt,
		NextBaseFee: r.NextBaseFee.Int,
		NodeTip:     r.NodeTip.Int,
		L1Fees:      map[string]*big.Int{},
	}
	if r.NativeUSD.Valid {
		answer, decimals, err := parseDecimal(r.NativeUSD.String)
		if err != nil {
			return entities.FeeSnapshot{}, err
		}
		snap.Price = &entities.PriceQuote{Answer: answer, Decimals: decimals, UpdatedAt: r.PriceUpdatedAt.Time}
	}
	var l1 map[string]string
	if err := json.Unmarshal(r.L1Fees, &l1); err != nil {
		return entities.FeeSnapshot{}, fmt.Errorf("decode l1 fees: %w", err)
	}
	for name, raw := range l1 {
		fee, ok := new(big.Int).SetString(raw, 10)
		if !ok {
			return entities.FeeSnapshot{}, fmt.Errorf("decode l1 fee %s %q", name, raw)
		}
		snap.L1Fees[name] = fee
	}
	return snap, nil
}

// decimalString writes answer / 10^decimals exactly, e.g. 312345000000 at 8 decimals is "3123.45000000".
func decimalString(answer *big.Int, decimals uint8) string {
	digits := answer.String()
	if decimals == 0 {
		return digits
	}
	if pad := int(decimals) + 1 - len(digits); pad > 0 {
		digits = strings.Repeat("0", pad) + digits
	}
	split := len(digits) - int(decimals)
	return digits[:split] + "." + digits[split:]
}

// parseDecimal reads a decimal string back as an integer and its scale.
func parseDecimal(s string) (*big.Int, uint8, error) {
	whole, frac, _ := strings.Cut(s, ".")
	if len(frac) > maxPriceDecimals {
		return nil, 0, fmt.Errorf("decimal %q has more than %d places", s, maxPriceDecimals)
	}
	n, ok := new(big.Int).SetString(whole+frac, 10)
	if !ok {
		return nil, 0, fmt.Errorf("parse decimal %q", s)
	}
	return n, uint8(len(frac)), nil
}

type postgresPredictionStore struct {
	db *sqlx.DB
}

// NewPostgresPredictionStore stores predictions in Postgres.
func NewPostgresPredictionStore(db *sqlx.DB) repositories.PredictionStore {
	return &postgresPredictionStore{db: db}
}

type pendingRow struct {
	ID              int64     `db:"id"`
	SnapshotID      int64     `db:"snapshot_id"`
	Network         string    `db:"network"`
	Predictor       string    `db:"predictor"`
	HorizonSeconds  int       `db:"horizon_seconds"`
	HorizonBlocks   int       `db:"horizon_blocks"`
	AtBlock         int64     `db:"at_block"`
	MaxFeePerGas    numeric   `db:"max_fee_per_gas"`
	TipPerGas       numeric   `db:"tip_per_gas"`
	ExpectedBaseFee numeric   `db:"expected_base_fee"`
	Status          string    `db:"status"`
	MadeAt          time.Time `db:"made_at"`
}

func (s *postgresPredictionStore) Insert(ctx context.Context, predictions []entities.Prediction) error {
	if len(predictions) == 0 {
		return nil
	}
	const cols = 10
	values := make([]string, 0, len(predictions))
	args := make([]any, 0, cols*len(predictions))
	for i, p := range predictions {
		ph := make([]string, cols)
		for j := range ph {
			ph[j] = fmt.Sprintf("$%d", cols*i+j+1)
		}
		values = append(values, "("+strings.Join(ph, ", ")+")")
		args = append(args, p.SnapshotID, p.Network, p.Predictor, p.Horizon.Seconds, p.Horizon.Blocks,
			int64(p.AtBlock), numeric{p.Bid.MaxFee}, numeric{p.Bid.Tip}, numeric{p.Bid.ExpectedBaseFee}, string(p.Status))
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO predictions
		(snapshot_id, network, predictor, horizon_seconds, horizon_blocks, at_block,
		 max_fee_per_gas, tip_per_gas, expected_base_fee, status)
		VALUES `+strings.Join(values, ", "), args...)
	if err != nil {
		return fmt.Errorf("insert predictions: %w", err)
	}
	return nil
}

func (s *postgresPredictionStore) Pending(ctx context.Context, network string) ([]entities.Prediction, error) {
	var rows []pendingRow
	err := s.db.SelectContext(ctx, &rows, `SELECT p.id, p.snapshot_id, p.network, p.predictor, p.horizon_seconds,
			p.horizon_blocks, p.at_block, p.max_fee_per_gas, p.tip_per_gas, p.expected_base_fee, p.status,
			s.observed_at AS made_at
		FROM predictions p JOIN snapshots s ON s.id = p.snapshot_id
		WHERE p.network = $1 AND p.status = 'pending' ORDER BY p.id`, network)
	if err != nil {
		return nil, fmt.Errorf("select pending predictions: %w", err)
	}
	out := make([]entities.Prediction, len(rows))
	for i, r := range rows {
		out[i] = entities.Prediction{
			ID:         r.ID,
			SnapshotID: r.SnapshotID,
			Network:    r.Network,
			Predictor:  r.Predictor,
			Horizon:    entities.Horizon{Seconds: r.HorizonSeconds, Blocks: r.HorizonBlocks},
			AtBlock:    uint64(r.AtBlock),
			MadeAt:     r.MadeAt,
			Bid:        entities.Bid{Tip: r.TipPerGas.Int, MaxFee: r.MaxFeePerGas.Int, ExpectedBaseFee: r.ExpectedBaseFee.Int},
			Status:     entities.PredictionStatus(r.Status),
		}
	}
	return out, nil
}

func (s *postgresPredictionStore) Settle(ctx context.Context, predictions []entities.Prediction) error {
	if len(predictions) == 0 {
		return nil
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `UPDATE predictions SET status = $2, resolved_at = $3, included = $4,
		inclusion_block = $5, paid_per_gas = $6, required_per_gas = $7, overpay_per_gas = $8, estimate_error_ratio = $9
		WHERE id = $1 AND status = 'pending'`)
	if err != nil {
		return fmt.Errorf("prepare settle: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for _, p := range predictions {
		var (
			included             sql.NullBool
			inclusion            sql.NullInt64
			paid, required, over numeric
			estimateError        sql.NullFloat64
		)
		if p.Score != nil {
			included = sql.NullBool{Bool: p.Score.Included, Valid: true}
			required = numeric{p.Score.Required()}
			if p.Score.Included {
				inclusion = sql.NullInt64{Int64: int64(p.Score.InclusionBlock), Valid: true}
				paid, over = numeric{p.Score.Paid()}, numeric{p.Score.Overpay()}
				estimateError = sql.NullFloat64{Float64: p.Score.EstimateError, Valid: true}
			}
		}
		if _, err := stmt.ExecContext(ctx, p.ID, string(p.Status), p.ResolvedAt, included, inclusion,
			paid, required, over, estimateError); err != nil {
			return fmt.Errorf("settle prediction %d: %w", p.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit settlements: %w", err)
	}
	return nil
}
