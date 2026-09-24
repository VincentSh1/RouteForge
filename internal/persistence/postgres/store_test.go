package postgres

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/persistence"
)

func TestInitialMigrationDefinesOperationalHistoryOnly(t *testing.T) {
	sqlBytes, err := migrationFiles.ReadFile("migrations/0001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(sqlBytes))
	for _, required := range []string{
		"create table routeforge_requests", "create table routeforge_provider_attempts",
		"timestamptz", "duration_us bigint", "ttfc_us bigint",
		"estimated_cost_micro_usd bigint", "on delete cascade",
		"primary key (request_id, attempt_number)", "routeforge_requests_started_at_idx",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration is missing %q", required)
		}
	}
	for _, prohibited := range []string{
		"prompt", "messages", "response_content", "request_body", "response_body",
		"authorization", "api_key", "user_id", "ip_address", "raw_error", "trace_id",
	} {
		if strings.Contains(sql, prohibited) {
			t.Errorf("migration contains prohibited field %q", prohibited)
		}
	}
}

func TestCacheMigrationAddsOnlyOperationalFlag(t *testing.T) {
	data, err := migrationFiles.ReadFile("migrations/0002_cache_hit.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(data))
	if !strings.Contains(sql, "add column cache_hit boolean not null default false") {
		t.Fatal("missing cache history flag")
	}
	for _, forbidden := range []string{"cache_key", "hash", "content", "prompt", "response"} {
		if strings.Contains(sql, forbidden) {
			t.Fatal("cache migration stores sensitive metadata")
		}
	}
}

func TestValidateRecordPreservesNullableOperationalValues(t *testing.T) {
	now := time.Now()
	record := persistence.RequestRecord{
		RequestID: "rfreq_test", StartedAt: now, CompletedAt: now,
		RoutingPolicy: "deterministic", LogicalModel: "routeforge/general", Outcome: "success",
		AttemptCount: 1,
		Attempts: []persistence.AttemptRecord{{
			AttemptNumber: 1, Provider: "mock", ResolvedProviderModel: "mock-model",
			StartedAt: now, CompletedAt: now, Outcome: "success",
		}},
	}
	if err := validateRecord(record); err != nil {
		t.Fatalf("validateRecord() error = %v", err)
	}
	if record.Attempts[0].TTFCUS != nil || record.Attempts[0].InputTokens != nil ||
		record.Attempts[0].EstimatedCostMicroUSD != nil {
		t.Fatal("unavailable values did not remain nullable")
	}
}

func TestPostgresUint64RejectsValuesOutsideBigint(t *testing.T) {
	tooLarge := uint64(math.MaxInt64) + 1
	if _, err := postgresUint64(&tooLarge); err == nil {
		t.Fatal("postgresUint64() error = nil")
	}
	maximum := uint64(math.MaxInt64)
	converted, err := postgresUint64(&maximum)
	if err != nil || converted == nil || *converted != math.MaxInt64 {
		t.Fatalf("postgresUint64() = %v, %v", converted, err)
	}
	if converted, err := postgresUint64(nil); err != nil || converted != nil {
		t.Fatalf("postgresUint64(nil) = %v, %v", converted, err)
	}
}

func TestOpenSanitizesInvalidDatabaseConfiguration(t *testing.T) {
	secretURL := "://database-secret"
	_, err := Open(context.Background(), secretURL)
	if err == nil || strings.Contains(err.Error(), secretURL) || strings.Contains(err.Error(), "database-secret") {
		t.Fatalf("Open() error = %v", err)
	}
}

func TestRecordBatchKeepsOneRequestAndOrderedParameterizedAttempts(t *testing.T) {
	now := time.Unix(1, 0)
	record := persistence.RequestRecord{RequestID: "rfreq_synthetic", StartedAt: now, CompletedAt: now, RoutingPolicy: "deterministic", LogicalModel: "mock-model", Outcome: "success", AttemptCount: 2, FallbackCount: 1}
	for i := range 2 {
		record.Attempts = append(record.Attempts, persistence.AttemptRecord{AttemptNumber: i + 1, Provider: "mock", ResolvedProviderModel: "mock-model", StartedAt: now, CompletedAt: now, Outcome: "success", Fallback: i == 1})
	}
	batch, err := recordBatch(record)
	if err != nil || len(batch.QueuedQueries) != 3 {
		t.Fatal("incorrect request batch")
	}
	for i, query := range batch.QueuedQueries {
		if strings.Contains(query.SQL, record.RequestID) || !strings.Contains(query.SQL, "$1") || query.Arguments[0] != record.RequestID {
			t.Fatal("SQL must remain parameterized")
		}
		if i > 0 && query.Arguments[1] != i {
			t.Fatal("attempt order changed")
		}
	}
	if !strings.Contains(batch.QueuedQueries[0].SQL, "INSERT INTO routeforge_requests") {
		t.Fatal("parent must precede attempts")
	}
	tooLarge := uint64(math.MaxInt64) + 1
	record.Attempts[1].InputTokens = &tooLarge
	if batch, err := recordBatch(record); err == nil || batch != nil {
		t.Fatal("invalid final attempt produced executable batch")
	}
	record.Attempts = nil
	record.AttemptCount = 0
	record.FallbackCount = 0
	record.CacheHit = true
	if batch, err := recordBatch(record); err != nil || len(batch.QueuedQueries) != 1 {
		t.Fatal("cache hit fabricated attempts")
	}
}
