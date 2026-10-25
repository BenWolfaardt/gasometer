// Package repositories implements the domain's stores on Postgres.
package repositories

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"math/big"

	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the pgx database/sql driver
	"github.com/jmoiron/sqlx"
)

//go:embed migrations/*.sql
var migrations embed.FS

const driverName = "pgx"

// Open connects to Postgres and checks the connection.
func Open(ctx context.Context, url string) (*sqlx.DB, error) {
	db, err := sqlx.Open(driverName, url)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return db, nil
}

// Migrate applies every pending migration. It uses its own connection pool because the migrate driver closes the
// pool it is given.
func Migrate(url string) (err error) {
	db, err := sql.Open(driverName, url)
	if err != nil {
		return fmt.Errorf("open database for migrations: %w", err)
	}
	dbDriver, err := pgxmigrate.WithInstance(db, &pgxmigrate.Config{})
	if err != nil {
		_ = db.Close()
		return fmt.Errorf("migration driver: %w", err)
	}
	source, err := iofs.New(migrations, "migrations")
	if err != nil {
		_ = dbDriver.Close()
		return fmt.Errorf("migration source: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", source, "pgx5", dbDriver)
	if err != nil {
		_ = source.Close()
		_ = dbDriver.Close()
		return fmt.Errorf("migrator: %w", err)
	}
	defer func() {
		srcErr, dbErr := m.Close()
		err = errors.Join(err, srcErr, dbErr)
	}()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// numeric maps a NUMERIC column to *big.Int through its decimal text form.
type numeric struct {
	*big.Int
}

func (n numeric) Value() (driver.Value, error) {
	if n.Int == nil {
		return nil, nil
	}
	return n.String(), nil
}

func (n *numeric) Scan(src any) error {
	if src == nil {
		n.Int = nil
		return nil
	}
	var s string
	switch v := src.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("scan numeric from %T", src)
	}
	i, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return fmt.Errorf("scan numeric %q", s)
	}
	n.Int = i
	return nil
}
