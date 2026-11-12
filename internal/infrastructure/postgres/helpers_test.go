//go:build integration

package postgres_test

import (
	"math/big"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func wei(v int64) *big.Int { return big.NewInt(v) }

func eqWei(a *big.Int, b *big.Int) bool { return a != nil && b != nil && a.Cmp(b) == 0 }
