//go:build integration

package postgres_test

import (
	"database/sql"
	"testing"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/postgres"
	"github.com/BenWolfaardt/gasometer/postgres/pgtest"
)

type probeRow struct {
	Network       string         `db:"network"`
	Rung          int            `db:"rung"`
	Account       string         `db:"account"`
	Nonce         int64          `db:"nonce"`
	Tip           string         `db:"tip_wei"`
	MaxFee        string         `db:"max_fee_wei"`
	TxHash        sql.NullString `db:"tx_hash"`
	HeadAtSend    int64          `db:"head_at_send"`
	IncludedBlock sql.NullInt64  `db:"included_block"`
	IncludedAtMs  sql.NullInt64  `db:"included_at_ms"`
	Outcome       string         `db:"outcome"`
	Error         sql.NullString `db:"error"`
	ReplacedBy    sql.NullString `db:"replaced_by"`
}

func TestProbeStoreCheck(t *testing.T) {
	db := pgtest.NewDatabase(t)
	store := postgres.NewProbeStore(db)
	if err := store.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `DROP TABLE probes`); err != nil {
		t.Fatal(err)
	}
	if err := store.Check(t.Context()); err == nil {
		t.Fatal("no error without the probes table")
	}
}

func TestProbeStoreInsert(t *testing.T) {
	db := pgtest.NewDatabase(t)
	store := postgres.NewProbeStore(db)
	block, at := uint64(98_700_001), uint64(1_761_500_000_123)
	included := gasometer.Probe{
		Network: "cchain", Rung: 8, Account: "0xabc", Nonce: 41, Tip: wei(1_000_000_000), MaxFee: wei(6_000_000_000),
		TxHash: "0x01", SentAt: t0, HeadAtSend: 98_700_000, IncludedBlock: &block, IncludedAtMillis: &at,
		Outcome: gasometer.ProbeIncluded,
	}
	rejected := gasometer.Probe{
		Network: "cchain", Rung: 1, Account: "0xdef", Tip: wei(0), MaxFee: wei(5_000_000_000), SentAt: t0,
		HeadAtSend: 98_700_000, Outcome: gasometer.ProbeRejected, Error: "transaction underpriced",
	}
	replaced := gasometer.Probe{
		Network: "cchain", Rung: 2, Account: "0x123", Tip: wei(1), MaxFee: wei(5_000_000_001), TxHash: "0x02",
		SentAt: t0, HeadAtSend: 98_700_000, Outcome: gasometer.ProbeReplaced, ReplacedBy: "0x03",
	}
	for _, p := range []gasometer.Probe{included, rejected, replaced} {
		if err := store.Insert(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}
	var rows []probeRow
	if err := db.SelectContext(t.Context(), &rows, `SELECT network, rung, account, nonce, tip_wei::TEXT AS tip_wei,
		max_fee_wei::TEXT AS max_fee_wei, tx_hash, head_at_send, included_block, included_at_ms, outcome, error,
		replaced_by FROM probes ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("%d rows", len(rows))
	}
	if r := rows[0]; r.Network != "cchain" || r.Rung != 8 || r.Account != "0xabc" || r.Nonce != 41 ||
		r.Tip != "1000000000" || r.MaxFee != "6000000000" || r.TxHash.String != "0x01" || r.HeadAtSend != 98_700_000 ||
		r.IncludedBlock.Int64 != 98_700_001 || r.IncludedAtMs.Int64 != 1_761_500_000_123 || r.Outcome != "included" ||
		r.Error.Valid || r.ReplacedBy.Valid {
		t.Fatalf("included row %+v", r)
	}
	if r := rows[1]; r.TxHash.Valid || r.IncludedBlock.Valid || r.IncludedAtMs.Valid || r.Outcome != "rejected" ||
		r.Error.String != "transaction underpriced" || r.Tip != "0" {
		t.Fatalf("rejected row %+v", r)
	}
	if r := rows[2]; r.Outcome != "replaced" || r.ReplacedBy.String != "0x03" || r.IncludedBlock.Valid {
		t.Fatalf("replaced row %+v", r)
	}
	var sentAt sql.NullTime
	if err := db.GetContext(t.Context(), &sentAt, `SELECT sent_at FROM probes WHERE rung = 8`); err != nil ||
		!sentAt.Time.Equal(t0) {
		t.Fatalf("sent_at %v, err %v", sentAt, err)
	}
}
