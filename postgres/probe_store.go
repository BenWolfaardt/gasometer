package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jmoiron/sqlx"

	"github.com/BenWolfaardt/gasometer"
)

var _ gasometer.ProbeStore = (*ProbeStore)(nil)

// ProbeStore stores probe transactions.
type ProbeStore struct {
	db *sqlx.DB
}

// NewProbeStore returns a ProbeStore on db.
func NewProbeStore(db *sqlx.DB) *ProbeStore {
	return &ProbeStore{db: db}
}

// Check fails when the probes table is missing. The gasometer service owns the schema, so the probe never migrates:
// a database migrated past what a deployed service knows would stop that service starting.
func (s *ProbeStore) Check(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `SELECT 1 FROM probes LIMIT 0`); err != nil {
		return fmt.Errorf("probes table missing; deploy gasometer with migration 000002 first: %w", err)
	}
	return nil
}

// Insert implements gasometer.ProbeStore.
func (s *ProbeStore) Insert(ctx context.Context, p gasometer.Probe) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO probes
		(network, rung, account, nonce, tip_wei, max_fee_wei, tx_hash, sent_at, head_at_send, included_block,
		 included_at_ms, outcome, error, replaced_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		p.Network, p.Rung, p.Account, int64(p.Nonce), numeric{p.Tip}, numeric{p.MaxFee}, nullString(p.TxHash),
		p.SentAt, int64(p.HeadAtSend), nullInt(p.IncludedBlock), nullInt(p.IncludedAtMillis), string(p.Outcome),
		nullString(p.Error), nullString(p.ReplacedBy))
	if err != nil {
		return fmt.Errorf("insert probe: %w", err)
	}
	return nil
}

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func nullInt(v *uint64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*v), Valid: true}
}
