package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/VincentSh1/RouteForge/internal/persistence"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const migrationLockID int64 = 736318207101

//go:embed migrations/*.sql
var migrationFiles embed.FS

var migrations = []struct {
	version int64
	path    string
}{
	{version: 1, path: "migrations/0001_initial.sql"},
}

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL configuration")
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, errors.New("PostgreSQL pool initialization failed")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("PostgreSQL connectivity validation failed")
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, errors.New("PostgreSQL migration failed")
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

func (s *Store) Write(ctx context.Context, record persistence.RequestRecord) error {
	if err := validateRecord(record); err != nil {
		return err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()

	_, err = tx.Exec(ctx, `
		INSERT INTO routeforge_requests (
			request_id, started_at, completed_at, routing_policy, streaming,
			logical_model, initial_provider, final_provider, outcome,
			attempt_count, fallback_count, request_duration_us
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, record.RequestID, record.StartedAt.UTC(), record.CompletedAt.UTC(), record.RoutingPolicy,
		record.Streaming, record.LogicalModel, record.InitialProvider, record.FinalProvider,
		record.Outcome, record.AttemptCount, record.FallbackCount, record.DurationUS)
	if err != nil {
		return err
	}

	for _, attempt := range record.Attempts {
		input, err := postgresUint64(attempt.InputTokens)
		if err != nil {
			return err
		}
		output, err := postgresUint64(attempt.OutputTokens)
		if err != nil {
			return err
		}
		total, err := postgresUint64(attempt.TotalTokens)
		if err != nil {
			return err
		}
		cost, err := postgresUint64(attempt.EstimatedCostMicroUSD)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO routeforge_provider_attempts (
				request_id, attempt_number, provider, resolved_provider_model,
				fallback, started_at, completed_at, duration_us, ttfc_us, outcome,
				input_tokens, output_tokens, total_tokens, estimated_cost_micro_usd
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		`, record.RequestID, attempt.AttemptNumber, attempt.Provider, attempt.ResolvedProviderModel,
			attempt.Fallback, attempt.StartedAt.UTC(), attempt.CompletedAt.UTC(), attempt.DurationUS,
			attempt.TTFCUS, attempt.Outcome, input, output, total, cost)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockID); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS routeforge_schema_migrations (
			version BIGINT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`); err != nil {
		return err
	}
	var unsupported bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM routeforge_schema_migrations WHERE version <> 1)").Scan(&unsupported); err != nil {
		return err
	}
	if unsupported {
		return errors.New("unsupported persistence schema version")
	}
	for _, migration := range migrations {
		var applied bool
		if err := tx.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM routeforge_schema_migrations WHERE version = $1)",
			migration.version,
		).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		sql, err := migrationFiles.ReadFile(migration.path)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO routeforge_schema_migrations (version) VALUES ($1)", migration.version); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
	}
	return tx.Commit(ctx)
}

func validateRecord(record persistence.RequestRecord) error {
	if record.RequestID == "" || len(record.RequestID) > 64 || record.RoutingPolicy == "" || len(record.RoutingPolicy) > 32 ||
		len([]rune(record.LogicalModel)) > 256 || record.Outcome == "" || len(record.Outcome) > 32 {
		return errors.New("invalid request persistence metadata")
	}
	if record.CompletedAt.Before(record.StartedAt) || record.DurationUS < 0 || record.AttemptCount != len(record.Attempts) ||
		record.FallbackCount < 0 || record.FallbackCount > record.AttemptCount {
		return errors.New("invalid request persistence timing or counts")
	}
	for i, attempt := range record.Attempts {
		if attempt.AttemptNumber != i+1 || attempt.Provider == "" || len([]rune(attempt.Provider)) > 64 ||
			attempt.ResolvedProviderModel == "" || len([]rune(attempt.ResolvedProviderModel)) > 256 ||
			attempt.Outcome == "" || len(attempt.Outcome) > 32 || attempt.CompletedAt.Before(attempt.StartedAt) ||
			attempt.DurationUS < 0 || (attempt.TTFCUS != nil && *attempt.TTFCUS < 0) {
			return errors.New("invalid provider attempt persistence metadata")
		}
	}
	return nil
}

func postgresUint64(value *uint64) (*int64, error) {
	if value == nil {
		return nil, nil
	}
	if *value > math.MaxInt64 {
		return nil, fmt.Errorf("persistence integer exceeds PostgreSQL BIGINT")
	}
	converted := int64(*value)
	return &converted, nil
}
