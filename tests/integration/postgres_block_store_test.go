//go:build integration

package integration_test

import (
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/infrastructure/repositories"
)

var t0 = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func blockRange(from, to uint64, at time.Time) []entities.BlockFee {
	var out []entities.BlockFee
	for n := from; n <= to; n++ {
		out = append(out, entities.BlockFee{
			Number: n, BaseFee: wei(int64(n) * 10), GasUsedRatio: 0.25,
			RequiredTip: wei(int64(n % 5)), MedianTip: wei(int64(n % 7)), ObservedAt: at,
		})
	}
	return out
}

func TestBlockStoreRoundTrip(t *testing.T) {
	store := repositories.NewPostgresBlockStore(newDatabase(t))
	ctx := t.Context()

	if _, ok, err := store.Latest(ctx, "base"); err != nil || ok {
		t.Fatalf("empty store: ok %v, err %v", ok, err)
	}
	huge, _ := new(big.Int).SetString("115792089237316195423570985008687907853269984665640564039457584007913129639935", 10)
	blocks := blockRange(1, 2_500, t0)
	blocks[0].BaseFee = huge
	if err := store.Upsert(ctx, "base", blocks); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(ctx, "op", blockRange(1, 3, t0)); err != nil {
		t.Fatal(err)
	}
	latest, ok, err := store.Latest(ctx, "base")
	if err != nil || !ok || latest != 2_500 {
		t.Fatalf("latest %d, ok %v, err %v", latest, ok, err)
	}
	got, err := store.Range(ctx, "base", 1, 3)
	if err != nil || len(got) != 3 {
		t.Fatalf("range %d, err %v", len(got), err)
	}
	if !eqWei(got[0].BaseFee, huge) || got[1].Number != 2 || !eqWei(got[1].RequiredTip, wei(2)) ||
		!eqWei(got[1].MedianTip, wei(2)) || got[1].GasUsedRatio != 0.25 || !got[1].ObservedAt.Equal(t0) {
		t.Fatalf("round trip %+v", got[:2])
	}
}

func TestBlockStoreUpsertReplaces(t *testing.T) {
	store := repositories.NewPostgresBlockStore(newDatabase(t))
	ctx := t.Context()
	if err := store.Upsert(ctx, "base", blockRange(1, 3, t0)); err != nil {
		t.Fatal(err)
	}
	reorg := blockRange(2, 2, t0.Add(time.Minute))
	reorg[0].BaseFee = wei(999)
	if err := store.Upsert(ctx, "base", reorg); err != nil {
		t.Fatal(err)
	}
	got, err := store.Range(ctx, "base", 1, 3)
	if err != nil || len(got) != 3 || !eqWei(got[1].BaseFee, wei(999)) || !got[1].ObservedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("got %+v, err %v", got, err)
	}
}

func TestBlockStoreRangeOmitsMissingBlocks(t *testing.T) {
	store := repositories.NewPostgresBlockStore(newDatabase(t))
	ctx := t.Context()
	if err := store.Upsert(ctx, "base", append(blockRange(1, 2, t0), blockRange(5, 6, t0)...)); err != nil {
		t.Fatal(err)
	}
	got, err := store.Range(ctx, "base", 1, 6)
	if err != nil || len(got) != 4 || got[2].Number != 5 {
		t.Fatalf("got %d blocks, err %v", len(got), err)
	}
}

func TestBlockStoreAverageBlockTime(t *testing.T) {
	store := repositories.NewPostgresBlockStore(newDatabase(t))
	ctx := t.Context()

	if _, err := store.AverageBlockTime(ctx, "base"); !errors.Is(err, entities.ErrInsufficientHistory) {
		t.Fatalf("empty: %v", err)
	}
	// The first poll backfills 1,024 blocks at one observation time.
	if err := store.Upsert(ctx, "base", blockRange(1, 1_024, t0)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AverageBlockTime(ctx, "base"); !errors.Is(err, entities.ErrInsufficientHistory) {
		t.Fatalf("one poll: %v", err)
	}
	// Then 30 blocks a minute: 2 s per block, whatever the backfill held.
	for i := range 3 {
		first := uint64(1_025 + 30*i)
		if err := store.Upsert(ctx, "base", blockRange(first, first+29, t0.Add(time.Duration(i+1)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.AverageBlockTime(ctx, "base")
	if err != nil || got != 2*time.Second {
		t.Fatalf("got %s, err %v", got, err)
	}
}

func TestBlockStorePrune(t *testing.T) {
	store := repositories.NewPostgresBlockStore(newDatabase(t))
	ctx := t.Context()
	if err := store.Upsert(ctx, "base", blockRange(1, 10, t0.Add(-49*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(ctx, "op", blockRange(1, 5, t0)); err != nil {
		t.Fatal(err)
	}
	n, err := store.Prune(ctx, t0.Add(-48*time.Hour))
	if err != nil || n != 10 {
		t.Fatalf("pruned %d, err %v", n, err)
	}
	if _, ok, _ := store.Latest(ctx, "base"); ok {
		t.Fatal("base blocks survived")
	}
	if latest, ok, _ := store.Latest(ctx, "op"); !ok || latest != 5 {
		t.Fatal("op blocks pruned")
	}
}
