package gasometertest

import (
	"errors"
	"fmt"
	"math/big"
	"slices"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer/internal/gasometer"
)

// callers is how many goroutines each concurrency test runs.
const callers = 8

// t0 has no sub-microsecond part, so it survives a Postgres TIMESTAMPTZ round trip unchanged.
var t0 = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

func wei(v int64) *big.Int { return big.NewInt(v) }

func sameWei(a, b *big.Int) bool { return a != nil && b != nil && a.Cmp(b) == 0 }

func block(n uint64, at time.Time) gasometer.BlockFee {
	return gasometer.BlockFee{
		Number: n, BaseFee: wei(int64(n) * 10), GasUsedRatio: 0.5,
		RequiredTip: wei(int64(n % 5)), MedianTip: wei(int64(n % 7)), ObservedAt: at,
	}
}

func blocks(at time.Time, numbers ...uint64) []gasometer.BlockFee {
	out := make([]gasometer.BlockFee, len(numbers))
	for i, n := range numbers {
		out[i] = block(n, at)
	}
	return out
}

func numbers(bs []gasometer.BlockFee) []uint64 {
	out := make([]uint64, len(bs))
	for i, b := range bs {
		out[i] = b.Number
	}
	return out
}

func sameBlock(a, b gasometer.BlockFee) bool {
	return a.Number == b.Number && sameWei(a.BaseFee, b.BaseFee) && a.GasUsedRatio == b.GasUsedRatio &&
		sameWei(a.RequiredTip, b.RequiredTip) && sameWei(a.MedianTip, b.MedianTip) && a.ObservedAt.Equal(b.ObservedAt)
}

// BlockStore runs the gasometer.BlockStore contract, with a new store from newStore for each subtest.
func BlockStore(t *testing.T, newStore func(t *testing.T) gasometer.BlockStore) {
	t.Run("upsert overwrites every field", func(t *testing.T) {
		store := newStore(t)
		if err := store.Upsert(t.Context(), "base", blocks(t0, 1)); err != nil {
			t.Fatal(err)
		}
		replacement := gasometer.BlockFee{
			Number: 1, BaseFee: wei(99), GasUsedRatio: 0.75, RequiredTip: wei(98), MedianTip: wei(97),
			ObservedAt: t0.Add(time.Hour),
		}
		if err := store.Upsert(t.Context(), "base", []gasometer.BlockFee{replacement}); err != nil {
			t.Fatal(err)
		}
		got, err := store.Range(t.Context(), "base", 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || !sameBlock(got[0], replacement) {
			t.Fatalf("after overwrite: %+v, want %+v", got, replacement)
		}
	})

	t.Run("upserting no blocks is a no-op", func(t *testing.T) {
		store := newStore(t)
		if err := store.Upsert(t.Context(), "base", blocks(t0, 1, 2)); err != nil {
			t.Fatal(err)
		}
		for _, none := range [][]gasometer.BlockFee{nil, {}} {
			if err := store.Upsert(t.Context(), "base", none); err != nil {
				t.Fatalf("upsert %v: %v", none, err)
			}
		}
		got, err := store.Range(t.Context(), "base", 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(numbers(got), []uint64{1, 2}) {
			t.Fatalf("blocks %v", numbers(got))
		}
	})

	t.Run("range is ascending and leaves missing blocks out", func(t *testing.T) {
		store := newStore(t)
		if err := store.Upsert(t.Context(), "base", blocks(t0, 5, 1, 3)); err != nil {
			t.Fatal(err)
		}
		if err := store.Upsert(t.Context(), "op", blocks(t0, 2)); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			network  string
			from, to uint64
			want     []uint64
		}{
			{"base", 1, 5, []uint64{1, 3, 5}},
			{"base", 2, 4, []uint64{3}},
			{"base", 6, 9, []uint64{}},
			{"op", 1, 5, []uint64{2}},
			{"cchain", 1, 5, []uint64{}},
		} {
			got, err := store.Range(t.Context(), c.network, c.from, c.to)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(numbers(got), c.want) {
				t.Errorf("Range(%s, %d, %d) = %v, want %v", c.network, c.from, c.to, numbers(got), c.want)
			}
		}
	})

	t.Run("holes are ascending and start above above", func(t *testing.T) {
		store := newStore(t)
		if err := store.Upsert(t.Context(), "base", blocks(t0, 10, 1, 6, 3)); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			above uint64
			want  []gasometer.BlockRange
		}{
			{0, []gasometer.BlockRange{{From: 2, To: 2}, {From: 4, To: 5}, {From: 7, To: 9}}},
			{4, []gasometer.BlockRange{{From: 5, To: 5}, {From: 7, To: 9}}},
			{10, []gasometer.BlockRange{}},
		} {
			got, err := store.Holes(t.Context(), "base", c.above)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("Holes(base, %d) = %v, want %v", c.above, got, c.want)
			}
		}
		got, err := store.Holes(t.Context(), "op", 0)
		if err != nil || len(got) != 0 {
			t.Fatalf("holes on a network with no blocks: %v, %v", got, err)
		}
	})

	t.Run("latest", func(t *testing.T) {
		store := newStore(t)
		if _, ok, err := store.Latest(t.Context(), "base"); err != nil || ok {
			t.Fatalf("empty store: ok %v, err %v", ok, err)
		}
		if err := store.Upsert(t.Context(), "base", blocks(t0, 4, 9, 2)); err != nil {
			t.Fatal(err)
		}
		if n, ok, err := store.Latest(t.Context(), "base"); err != nil || !ok || n != 9 {
			t.Fatalf("latest %d, ok %v, err %v", n, ok, err)
		}
		if _, ok, err := store.Latest(t.Context(), "op"); err != nil || ok {
			t.Fatalf("another network: ok %v, err %v", ok, err)
		}
	})

	t.Run("prune deletes across networks and counts", func(t *testing.T) {
		store := newStore(t)
		old, recent := t0, t0.Add(time.Hour)
		for network, bs := range map[string][]gasometer.BlockFee{
			"base": append(blocks(old, 1, 2), blocks(recent, 3)...),
			"op":   append(blocks(old, 1), blocks(recent, 2)...),
		} {
			if err := store.Upsert(t.Context(), network, bs); err != nil {
				t.Fatal(err)
			}
		}
		n, err := store.Prune(t.Context(), t0.Add(30*time.Minute))
		if err != nil || n != 3 {
			t.Fatalf("pruned %d, err %v, want 3", n, err)
		}
		for network, want := range map[string][]uint64{"base": {3}, "op": {2}} {
			got, err := store.Range(t.Context(), network, 0, 10)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(numbers(got), want) {
				t.Errorf("%s after prune: %v, want %v", network, numbers(got), want)
			}
		}
	})

	t.Run("average block time needs history", func(t *testing.T) {
		store := newStore(t)
		if _, err := store.AverageBlockTime(t.Context(), "base"); !errors.Is(err, gasometer.ErrInsufficientHistory) {
			t.Fatalf("empty store: %v, want ErrInsufficientHistory", err)
		}
		if err := store.Upsert(t.Context(), "base", blocks(t0, 1)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AverageBlockTime(t.Context(), "base"); !errors.Is(err, gasometer.ErrInsufficientHistory) {
			t.Fatalf("one block: %v, want ErrInsufficientHistory", err)
		}
	})

	t.Run("concurrent writers and readers across networks", func(t *testing.T) {
		store := newStore(t)
		Concurrently(t, callers, func(i int) {
			network := fmt.Sprintf("net-%d", i%2)
			first := uint64(i/2*10 + 1)
			if err := store.Upsert(t.Context(), network, blocks(t0, first, first+1, first+2)); err != nil {
				t.Errorf("caller %d upsert: %v", i, err)
				return
			}
			if _, err := store.Range(t.Context(), network, 0, 100); err != nil {
				t.Errorf("caller %d range: %v", i, err)
			}
			if _, _, err := store.Latest(t.Context(), network); err != nil {
				t.Errorf("caller %d latest: %v", i, err)
			}
			if _, err := store.Holes(t.Context(), network, 0); err != nil {
				t.Errorf("caller %d holes: %v", i, err)
			}
		})
		for network, want := range map[string][]uint64{
			"net-0": {1, 2, 3, 11, 12, 13, 21, 22, 23, 31, 32, 33},
			"net-1": {1, 2, 3, 11, 12, 13, 21, 22, 23, 31, 32, 33},
		} {
			got, err := store.Range(t.Context(), network, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(numbers(got), want) {
				t.Errorf("%s after concurrent upserts: %v, want %v", network, numbers(got), want)
			}
		}
	})
}

func snapshot(network string, atBlock uint64) gasometer.FeeSnapshot {
	return gasometer.FeeSnapshot{
		Network: network, AtBlock: atBlock, ObservedAt: t0, NextBaseFee: wei(int64(atBlock) * 3), NodeTip: wei(2),
	}
}

// SnapshotStore runs the gasometer.SnapshotStore contract, with a new store from newStore for each subtest.
func SnapshotStore(t *testing.T, newStore func(t *testing.T) gasometer.SnapshotStore) {
	t.Run("insert returns unique ids that get finds", func(t *testing.T) {
		store := newStore(t)
		ids := make([]int64, 3)
		for i := range ids {
			id, err := store.Insert(t.Context(), snapshot("base", uint64(100+i)))
			if err != nil {
				t.Fatal(err)
			}
			ids[i] = id
		}
		if ids[0] == ids[1] || ids[1] == ids[2] || ids[0] == ids[2] {
			t.Fatalf("ids not unique: %v", ids)
		}
		got, err := store.Get(t.Context(), ids)
		if err != nil {
			t.Fatal(err)
		}
		for i, id := range ids {
			s, ok := got[id]
			if !ok || s.ID != id || s.Network != "base" || s.AtBlock != uint64(100+i) || !s.ObservedAt.Equal(t0) ||
				!sameWei(s.NextBaseFee, wei(int64(100+i)*3)) || !sameWei(s.NodeTip, wei(2)) {
				t.Errorf("snapshot %d: %+v, found %v", id, s, ok)
			}
		}
	})

	t.Run("ids never stored are absent", func(t *testing.T) {
		store := newStore(t)
		id, err := store.Insert(t.Context(), snapshot("base", 1))
		if err != nil {
			t.Fatal(err)
		}
		got, err := store.Get(t.Context(), []int64{id, id + 1_000})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got[id]; !ok || len(got) != 1 {
			t.Fatalf("got %v, want only %d", got, id)
		}
	})

	t.Run("no ids give an empty map", func(t *testing.T) {
		store := newStore(t)
		for _, ids := range [][]int64{nil, {}} {
			got, err := store.Get(t.Context(), ids)
			if err != nil || got == nil || len(got) != 0 {
				t.Fatalf("Get(%v) = %v, %v", ids, got, err)
			}
		}
	})

	t.Run("concurrent inserts get unique ids", func(t *testing.T) {
		store := newStore(t)
		const each = 4
		ids := make([][]int64, callers)
		Concurrently(t, callers, func(i int) {
			for j := range each {
				id, err := store.Insert(t.Context(), snapshot(fmt.Sprintf("net-%d", i%2), uint64(i*each+j)))
				if err != nil {
					t.Errorf("caller %d: %v", i, err)
					return
				}
				ids[i] = append(ids[i], id)
			}
		})
		all := slices.Concat(ids...)
		slices.Sort(all)
		if len(slices.Compact(slices.Clone(all))) != callers*each {
			t.Fatalf("%d unique ids from %d inserts", len(slices.Compact(slices.Clone(all))), callers*each)
		}
		got, err := store.Get(t.Context(), all)
		if err != nil || len(got) != callers*each {
			t.Fatalf("get found %d of %d, err %v", len(got), callers*each, err)
		}
	})
}

func prediction(snapshotID int64, network, predictor string) gasometer.Prediction {
	return gasometer.Prediction{
		SnapshotID: snapshotID, Network: network, Predictor: predictor,
		Horizon: gasometer.Horizon{Seconds: 12, Blocks: 6}, AtBlock: 100, MadeAt: t0,
		Bid:    gasometer.Bid{Tip: wei(1), MaxFee: wei(30), ExpectedBaseFee: wei(10)},
		Status: gasometer.PredictionPending,
	}
}

func settled(p gasometer.Prediction) gasometer.Prediction {
	p.Status = gasometer.PredictionResolved
	p.ResolvedAt = t0.Add(time.Minute)
	p.Score = &gasometer.Score{RequiredBaseFee: wei(10), RequiredTip: wei(1)}
	return p
}

func predictors(ps []gasometer.Prediction) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Predictor
	}
	return out
}

// PredictionStore runs the gasometer.PredictionStore contract, with new stores from newStores for each subtest. The
// snapshot store holds the snapshots the predictions refer to.
func PredictionStore(t *testing.T, newStores func(t *testing.T) (gasometer.SnapshotStore, gasometer.PredictionStore)) {
	insertSnapshot := func(t *testing.T, snapshots gasometer.SnapshotStore, network string) int64 {
		t.Helper()
		id, err := snapshots.Insert(t.Context(), snapshot(network, 100))
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	t.Run("pending filters by network in insertion order", func(t *testing.T) {
		snapshots, store := newStores(t)
		base, op := insertSnapshot(t, snapshots, "base"), insertSnapshot(t, snapshots, "op")
		for _, batch := range [][]gasometer.Prediction{
			{prediction(base, "base", "a"), prediction(op, "op", "b"), prediction(base, "base", "c")},
			{prediction(base, "base", "d")},
		} {
			if err := store.Insert(t.Context(), batch); err != nil {
				t.Fatal(err)
			}
		}
		for network, want := range map[string][]string{"base": {"a", "c", "d"}, "op": {"b"}, "cchain": {}} {
			got, err := store.Pending(t.Context(), network)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(predictors(got), want) {
				t.Errorf("Pending(%s) = %v, want %v", network, predictors(got), want)
			}
			for _, p := range got {
				if p.ID == 0 || p.SnapshotID == 0 || p.Status != gasometer.PredictionPending || p.Horizon.Blocks != 6 ||
					!sameWei(p.Bid.MaxFee, wei(30)) {
					t.Errorf("pending prediction %+v", p)
				}
			}
		}
	})

	t.Run("settled predictions leave pending", func(t *testing.T) {
		snapshots, store := newStores(t)
		id := insertSnapshot(t, snapshots, "base")
		batch := []gasometer.Prediction{prediction(id, "base", "a"), prediction(id, "base", "b"), prediction(id, "base", "c")}
		if err := store.Insert(t.Context(), batch); err != nil {
			t.Fatal(err)
		}
		pending, err := store.Pending(t.Context(), "base")
		if err != nil || len(pending) != 3 {
			t.Fatalf("pending %d, err %v", len(pending), err)
		}
		if err := store.Settle(t.Context(), []gasometer.Prediction{settled(pending[1])}); err != nil {
			t.Fatal(err)
		}
		got, err := store.Pending(t.Context(), "base")
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(predictors(got), []string{"a", "c"}) {
			t.Fatalf("pending after settle %v, want [a c]", predictors(got))
		}
	})

	t.Run("concurrent inserts and settles", func(t *testing.T) {
		snapshots, store := newStores(t)
		ids := make([]int64, callers)
		for i := range ids {
			ids[i] = insertSnapshot(t, snapshots, fmt.Sprintf("net-%d", i))
		}
		Concurrently(t, callers, func(i int) {
			network := fmt.Sprintf("net-%d", i)
			batch := []gasometer.Prediction{prediction(ids[i], network, "keep"), prediction(ids[i], network, "settle")}
			if err := store.Insert(t.Context(), batch); err != nil {
				t.Errorf("caller %d insert: %v", i, err)
				return
			}
			pending, err := store.Pending(t.Context(), network)
			if err != nil || len(pending) != 2 {
				t.Errorf("caller %d pending %d, err %v", i, len(pending), err)
				return
			}
			if err := store.Settle(t.Context(), []gasometer.Prediction{settled(pending[1])}); err != nil {
				t.Errorf("caller %d settle: %v", i, err)
			}
		})
		for i := range callers {
			got, err := store.Pending(t.Context(), fmt.Sprintf("net-%d", i))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(predictors(got), []string{"keep"}) {
				t.Errorf("net-%d pending %v, want [keep]", i, predictors(got))
			}
		}
	})
}
