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
