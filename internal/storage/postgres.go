package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func NewPostgres(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse config: %w", err)
	}

	cfg.MaxConns = 20
	cfg.MinConns = 2
	cfg.MaxConnIdleTime = 5 * time.Minute

	var pool *pgxpool.Pool
	for attempt := 1; attempt <= 5; attempt++ {
		pool, err = pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			if pingErr := pool.Ping(ctx); pingErr == nil {
				return pool, nil
			}
			pool.Close()
		}
		time.Sleep(time.Duration(attempt) * time.Second)
	}

	return nil, fmt.Errorf("postgres: connection failed after 5 attempts: %w", err)
}
