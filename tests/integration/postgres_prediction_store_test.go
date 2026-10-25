//go:build integration

package integration_test

import (
	"math/big"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/infrastructure/repositories"
)

func TestSnapshotStoreRoundTrip(t *testing.T) {
	store := repositories.NewPostgresSnapshotStore(newDatabase(t))
	ctx := t.Context()
	withPrice := entities.FeeSnapshot{
		Network: "base", AtBlock: 100, ObservedAt: t0, NextBaseFee: wei(5_000_000), NodeTip: wei(1_000_000),
		Price:  &entities.PriceQuote{Answer: wei(312_345_000_000), Decimals: 8, UpdatedAt: t0.Add(-time.Minute)},
		L1Fees: map[string]*big.Int{"native": wei(491_374_408), "erc20": wei(491_374_408)},
	}
	withoutPrice := entities.FeeSnapshot{Network: "cchain", AtBlock: 7, ObservedAt: t0, NextBaseFee: wei(1), NodeTip: wei(150)}

	id1, err := store.Insert(ctx, withPrice)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := store.Insert(ctx, withoutPrice)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, []int64{id1, id2, 999})
	if err != nil || len(got) != 2 {
		t.Fatalf("got %d, err %v", len(got), err)
	}
	a := got[id1]
	if a.ID != id1 || a.Network != "base" || a.AtBlock != 100 || !a.ObservedAt.Equal(t0) ||
		!eqWei(a.NextBaseFee, wei(5_000_000)) || !eqWei(a.NodeTip, wei(1_000_000)) || !eqWei(a.L1Fees["erc20"], wei(491_374_408)) {
		t.Fatalf("snapshot %+v", a)
	}
	if a.Price == nil || a.Price.USD() != 3_123.45 || !a.Price.UpdatedAt.Equal(t0.Add(-time.Minute)) {
		t.Fatalf("price %+v", a.Price)
	}
	if b := got[id2]; b.Price != nil || len(b.L1Fees) != 0 {
		t.Fatalf("snapshot without price %+v", b)
	}
	if empty, err := store.Get(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty get: %v %v", empty, err)
	}
}

func TestSnapshotStoreRejectsTooManyDecimals(t *testing.T) {
	store := repositories.NewPostgresSnapshotStore(newDatabase(t))
	_, err := store.Insert(t.Context(), entities.FeeSnapshot{
		Network: "base", ObservedAt: t0, NextBaseFee: wei(1), NodeTip: wei(1),
		Price: &entities.PriceQuote{Answer: wei(1), Decimals: 19, UpdatedAt: t0},
	})
	if err == nil {
		t.Fatal("want error")
	}
}

func TestPredictionStoreLifecycle(t *testing.T) {
	db := newDatabase(t)
	snapshots := repositories.NewPostgresSnapshotStore(db)
	predictions := repositories.NewPostgresPredictionStore(db)
	ctx := t.Context()

	snapID, err := snapshots.Insert(ctx, entities.FeeSnapshot{Network: "base", AtBlock: 100, ObservedAt: t0, NextBaseFee: wei(100), NodeTip: wei(10)})
	if err != nil {
		t.Fatal(err)
	}
	pending := func(predictor string, seconds, blocks int) entities.Prediction {
		return entities.Prediction{
			SnapshotID: snapID, Network: "base", Predictor: predictor, AtBlock: 100,
			Horizon: entities.Horizon{Seconds: seconds, Blocks: blocks},
			Bid:     entities.Bid{Tip: wei(10), MaxFee: wei(210), ExpectedBaseFee: wei(100)},
			Status:  entities.PredictionPending,
		}
	}
	if err := predictions.Insert(ctx, []entities.Prediction{
		pending("node", 2, 1), pending("wallet", 30, 15), pending("gasometer", 120, 60),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := predictions.Pending(ctx, "base")
	if err != nil || len(got) != 3 {
		t.Fatalf("pending %d, err %v", len(got), err)
	}
	p := got[0]
	if p.ID == 0 || p.Predictor != "node" || p.Horizon.Blocks != 1 || p.AtBlock != 100 || !p.MadeAt.Equal(t0) ||
		!eqWei(p.Bid.MaxFee, wei(210)) || !eqWei(p.Bid.Tip, wei(10)) || !eqWei(p.Bid.ExpectedBaseFee, wei(100)) {
		t.Fatalf("pending %+v", p)
	}
	if other, err := predictions.Pending(ctx, "op"); err != nil || len(other) != 0 {
		t.Fatalf("op pending %d, err %v", len(other), err)
	}

	included := got[0]
	included.Status, included.ResolvedAt = entities.PredictionResolved, t0.Add(time.Minute)
	included.Score = &entities.Score{
		Included: true, InclusionBlock: 101, PaidBaseFee: wei(100), PaidTip: wei(10),
		RequiredBaseFee: wei(100), RequiredTip: wei(5), EstimateError: 0.01,
	}
	missed := got[1]
	missed.Status, missed.ResolvedAt = entities.PredictionResolved, t0.Add(time.Minute)
	missed.Score = &entities.Score{RequiredBaseFee: wei(100), RequiredTip: wei(50)}
	expired := got[2]
	expired.Status, expired.ResolvedAt = entities.PredictionExpired, t0.Add(time.Hour)
	if err := predictions.Settle(ctx, []entities.Prediction{included, missed, expired}); err != nil {
		t.Fatal(err)
	}
	if left, err := predictions.Pending(ctx, "base"); err != nil || len(left) != 0 {
		t.Fatalf("still pending %d, err %v", len(left), err)
	}

	var rows []struct {
		Status    string   `db:"status"`
		Included  *bool    `db:"included"`
		Inclusion *int64   `db:"inclusion_block"`
		Paid      *string  `db:"paid_per_gas"`
		Required  *string  `db:"required_per_gas"`
		Overpay   *string  `db:"overpay_per_gas"`
		Error     *float64 `db:"estimate_error_ratio"`
	}
	if err := db.SelectContext(ctx, &rows, `SELECT status, included, inclusion_block, paid_per_gas::text,
		required_per_gas::text, overpay_per_gas::text, estimate_error_ratio FROM predictions ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	r := rows[0]
	if r.Status != "resolved" || !*r.Included || *r.Inclusion != 101 || *r.Paid != "110" || *r.Required != "105" ||
		*r.Overpay != "5" || *r.Error != 0.01 {
		t.Fatalf("included row %+v", r)
	}
	r = rows[1]
	if r.Status != "resolved" || *r.Included || r.Inclusion != nil || r.Paid != nil || *r.Required != "150" || r.Overpay != nil {
		t.Fatalf("missed row %+v", r)
	}
	r = rows[2]
	if r.Status != "expired" || r.Included != nil || r.Required != nil {
		t.Fatalf("expired row %+v", r)
	}

	// Settling twice leaves the first settlement in place.
	included.Score.InclusionBlock = 999
	if err := predictions.Settle(ctx, []entities.Prediction{included}); err != nil {
		t.Fatal(err)
	}
	var inclusion int64
	if err := db.GetContext(ctx, &inclusion, `SELECT inclusion_block FROM predictions WHERE id = $1`, included.ID); err != nil || inclusion != 101 {
		t.Fatalf("inclusion %d, err %v", inclusion, err)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	db := newDatabase(t)
	var version int
	if err := db.Get(&version, `SELECT version FROM schema_migrations`); err != nil || version != 1 {
		t.Fatalf("version %d, err %v", version, err)
	}
}
