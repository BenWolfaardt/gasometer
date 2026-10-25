// Package pgtest gives each test its own migrated Postgres schema, for the integration tests of every package that
// needs a database.
package pgtest

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/BenWolfaardt/gasometer/postgres"
)

// DatabaseURLEnv names the variable holding the test database's URL.
const DatabaseURLEnv = "GASOMETER_TEST_DATABASE_URL"

// NewDatabase opens a fresh, migrated schema for one test.
func NewDatabase(t testing.TB) *sqlx.DB {
	t.Helper()
	db, err := postgres.Open(t.Context(), NewDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// NewDatabaseURL migrates a fresh schema for one test, drops it afterwards, and returns a URL that selects it.
func NewDatabaseURL(t testing.TB) string {
	t.Helper()
	base := os.Getenv(DatabaseURLEnv)
	if base == "" {
		t.Fatalf("%s is not set; run make test-db-up", DatabaseURLEnv)
	}
	admin, err := postgres.Open(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	name := strings.ToLower(strings.NewReplacer("/", "_", "-", "_").Replace(t.Name()))
	schema := fmt.Sprintf("test_%s_%d", name, time.Now().UnixNano())
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

	if err := postgres.Migrate(u.String()); err != nil {
		t.Fatal(err)
	}
	return u.String()
}
