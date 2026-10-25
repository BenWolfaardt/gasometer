//go:build integration

package integration_test

import (
	"fmt"
	"math/big"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/BenWolfaardt/gasometer/internal/infrastructure/repositories"
)

const databaseURLEnv = "GASOMETER_TEST_DATABASE_URL"

// newDatabase opens a fresh, migrated schema for one test.
func newDatabase(t *testing.T) *sqlx.DB {
	t.Helper()
	db, err := repositories.Open(t.Context(), newDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newDatabaseURL migrates a fresh schema for one test, drops it afterwards, and returns a URL that selects it.
func newDatabaseURL(t *testing.T) string {
	t.Helper()
	base := os.Getenv(databaseURLEnv)
	if base == "" {
		t.Fatalf("%s is not set; run make test-db-up", databaseURLEnv)
	}
	admin, err := repositories.Open(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	schema := fmt.Sprintf("test_%s_%d", strings.ToLower(strings.NewReplacer("/", "_", "-", "_").Replace(t.Name())), time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })

	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()

	if err := repositories.Migrate(u.String()); err != nil {
		t.Fatal(err)
	}
	return u.String()
}

func wei(v int64) *big.Int { return big.NewInt(v) }

func eqWei(a *big.Int, b *big.Int) bool { return a != nil && b != nil && a.Cmp(b) == 0 }
