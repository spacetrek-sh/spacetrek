// Package postgres provides PostgreSQL database connectivity and repository implementations.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

// DB wraps sqlx.DB with application-specific methods.
type DB struct {
	*sqlx.DB
}

// Connect establishes a connection to PostgreSQL using the given connection string.
// It configures connection pooling and returns a DB instance.
func Connect(ctx context.Context, databaseURL string, maxConns int) (*DB, error) {
	db, err := sqlx.ConnectContext(ctx, "postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns / 2)
	db.SetConnMaxLifetime(time.Hour)
	db.SetConnMaxIdleTime(15 * time.Minute)

	return &DB{DB: db}, nil
}

// PingContext pings the database to verify the connection is alive.
func (db *DB) PingContext(ctx context.Context) error {
	return db.DB.PingContext(ctx)
}

// Close closes the database connection.
func (db *DB) Close() error {
	return db.DB.Close()
}
