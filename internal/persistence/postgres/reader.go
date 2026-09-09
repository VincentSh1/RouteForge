package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/VincentSh1/RouteForge/internal/persistence"
	"github.com/jackc/pgx/v5"
)

const requestColumns = `request_id, started_at, completed_at, routing_policy,
    streaming, logical_model, initial_provider, final_provider, outcome,
    attempt_count, fallback_count, request_duration_us, cache_hit`

// SQL structure is entirely application-owned; only values become parameters.
func listSQL(query persistence.ListQuery) (string, []any, error) {
	if query.Limit < 1 || query.Limit > persistence.MaxPageSize {
		return "", nil, errors.New("invalid history page size")
	}
	var conditions []string
	var args []any
	add := func(value any) string { args = append(args, value); return fmt.Sprintf("$%d", len(args)) }
	if query.Before != nil {
		stamp, id := add(query.Before.StartedAt.UTC()), add(query.Before.RequestID)
		conditions = append(conditions, "(started_at, request_id) < ("+stamp+", "+id+")")
	}
	if query.Provider != "" {
		param := add(query.Provider)
		conditions = append(conditions, "(initial_provider = "+param+" OR final_provider = "+param+")")
	}
	if query.RoutingPolicy != "" {
		conditions = append(conditions, "routing_policy = "+add(query.RoutingPolicy))
	}
	if query.Outcome != "" {
		conditions = append(conditions, "outcome = "+add(query.Outcome))
	}
	if query.Streaming != nil {
		conditions = append(conditions, "streaming = "+add(*query.Streaming))
	}
	if query.CacheHit != nil {
		conditions = append(conditions, "cache_hit = "+add(*query.CacheHit))
	}
	if query.StartedAfter != nil {
		conditions = append(conditions, "started_at > "+add(query.StartedAfter.UTC()))
	}
	if query.StartedBefore != nil {
		conditions = append(conditions, "started_at < "+add(query.StartedBefore.UTC()))
	}
	sql := "SELECT " + requestColumns + " FROM routeforge_requests"
	if len(conditions) > 0 {
		sql += " WHERE " + strings.Join(conditions, " AND ")
	}
	sql += " ORDER BY started_at DESC, request_id DESC LIMIT " + add(query.Limit+1)
	return sql, args, nil
}

type rowScanner interface{ Scan(...any) error }

func scanRequest(row rowScanner) (persistence.RequestRecord, error) {
	var record persistence.RequestRecord
	err := row.Scan(&record.RequestID, &record.StartedAt, &record.CompletedAt, &record.RoutingPolicy,
		&record.Streaming, &record.LogicalModel, &record.InitialProvider, &record.FinalProvider, &record.Outcome,
		&record.AttemptCount, &record.FallbackCount, &record.DurationUS, &record.CacheHit)
	record.StartedAt = record.StartedAt.UTC()
	record.CompletedAt = record.CompletedAt.UTC()
	return record, err
}

func (s *Store) List(ctx context.Context, query persistence.ListQuery) ([]persistence.RequestRecord, error) {
	ctx, cancel := context.WithTimeout(ctx, persistence.QueryTimeout)
	defer cancel()
	sql, args, err := listSQL(query)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, errors.New("history query failed")
	}
	defer rows.Close()
	records := make([]persistence.RequestRecord, 0, query.Limit+1)
	for rows.Next() {
		record, err := scanRequest(rows)
		if err != nil {
			return nil, errors.New("history query failed")
		}
		records = append(records, record)
	}
	if rows.Err() != nil {
		return nil, errors.New("history query failed")
	}
	return records, nil
}

func (s *Store) Detail(ctx context.Context, id string) (persistence.RequestRecord, error) {
	if !persistence.ValidRequestID(id) {
		return persistence.RequestRecord{}, errors.New("invalid request ID")
	}
	ctx, cancel := context.WithTimeout(ctx, persistence.QueryTimeout)
	defer cancel()
	// A repeatable read keeps the parent and children consistent if an operator
	// removes history directly while this read is in progress.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return persistence.RequestRecord{}, errors.New("history query failed")
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	record, err := scanRequest(tx.QueryRow(ctx, "SELECT "+requestColumns+" FROM routeforge_requests WHERE request_id = $1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return persistence.RequestRecord{}, persistence.ErrNotFound
	}
	if err != nil {
		return persistence.RequestRecord{}, errors.New("history query failed")
	}
	rows, err := tx.Query(ctx, `SELECT attempt_number, provider, resolved_provider_model, fallback,
        started_at, completed_at, duration_us, ttfc_us, outcome,
        input_tokens, output_tokens, total_tokens, estimated_cost_micro_usd
        FROM routeforge_provider_attempts WHERE request_id = $1 ORDER BY attempt_number ASC LIMIT $2`, id, persistence.MaxDetailAttempts+1)
	if err != nil {
		return persistence.RequestRecord{}, errors.New("history query failed")
	}
	defer rows.Close()
	record.Attempts = make([]persistence.AttemptRecord, 0)
	for rows.Next() {
		var attempt persistence.AttemptRecord
		err := rows.Scan(&attempt.AttemptNumber, &attempt.Provider, &attempt.ResolvedProviderModel, &attempt.Fallback,
			&attempt.StartedAt, &attempt.CompletedAt, &attempt.DurationUS, &attempt.TTFCUS, &attempt.Outcome,
			&attempt.InputTokens, &attempt.OutputTokens, &attempt.TotalTokens, &attempt.EstimatedCostMicroUSD)
		if err != nil {
			return persistence.RequestRecord{}, errors.New("history query failed")
		}
		attempt.StartedAt = attempt.StartedAt.UTC()
		attempt.CompletedAt = attempt.CompletedAt.UTC()
		record.Attempts = append(record.Attempts, attempt)
	}
	if rows.Err() != nil || len(record.Attempts) > persistence.MaxDetailAttempts {
		return persistence.RequestRecord{}, errors.New("history query failed")
	}
	if err := tx.Commit(ctx); err != nil {
		return persistence.RequestRecord{}, errors.New("history query failed")
	}
	return record, nil
}
