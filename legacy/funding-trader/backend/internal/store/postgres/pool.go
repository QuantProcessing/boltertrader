package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	ApplicationDBIdentity = "funding-trader-app"
	CutoverDBIdentity     = "funding-trader-cutover"
)

func ApplicationPoolConfig(rawURL string) (*pgxpool.Config, error) {
	cfg, err := poolConfig(rawURL, ApplicationDBIdentity)
	if err != nil {
		return nil, err
	}
	if cfg.MaxConns < 2 {
		cfg.MaxConns = 2
	}
	return cfg, nil
}

func CutoverPoolConfig(rawURL string) (*pgxpool.Config, error) {
	cfg, err := poolConfig(rawURL, CutoverDBIdentity)
	if err != nil {
		return nil, err
	}
	cfg.MinConns = 0
	cfg.MaxConns = 1
	return cfg, nil
}

func OpenApplicationPool(ctx context.Context, rawURL string) (*pgxpool.Pool, error) {
	cfg, err := ApplicationPoolConfig(rawURL)
	if err != nil {
		return nil, err
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}

func OpenCutoverPool(ctx context.Context, rawURL string) (*pgxpool.Pool, error) {
	cfg, err := CutoverPoolConfig(rawURL)
	if err != nil {
		return nil, err
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}

func poolConfig(rawURL, identity string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse postgres configuration: %w", err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = make(map[string]string)
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = identity
	return cfg, nil
}
