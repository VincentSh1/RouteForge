package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/persistence"
)

// Run only against the disposable Compose database. Ordinary Go tests remain
// offline. Diagnostics deliberately omit connection strings and SQL errors.
func TestPostgresAtomicWriteIntegration(t *testing.T) {
	databaseURL := os.Getenv("ROUTEFORGE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal("database initialization failed")
	}
	defer store.Close()
	id, err := persistence.NewRequestID()
	if err != nil {
		t.Fatal("request ID generation failed")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if _, err := store.pool.Exec(cleanup, "DELETE FROM routeforge_requests WHERE request_id=$1", id); err != nil {
			t.Error("test record cleanup failed")
		}
	}()
	now := time.Now().UTC()
	record := persistence.RequestRecord{RequestID: id, StartedAt: now, CompletedAt: now, RoutingPolicy: "deterministic", LogicalModel: "mock-model", Outcome: "success", AttemptCount: 2, FallbackCount: 1,
		Attempts: []persistence.AttemptRecord{
			{AttemptNumber: 1, Provider: "mock", ResolvedProviderModel: "mock-model", StartedAt: now, CompletedAt: now, Outcome: "timeout"},
			{AttemptNumber: 2, Provider: "mock", ResolvedProviderModel: "mock-model", StartedAt: now, CompletedAt: now, Outcome: "success", Fallback: true},
		},
	}
	// Fail a database constraint in the last child, after valid earlier inserts.
	invalidTTFC := int64(1)
	record.Attempts[1].TTFCUS = &invalidTTFC
	if err := store.Write(ctx, record); err == nil {
		t.Fatal("invalid final child was accepted")
	}
	assertRows := func(requests, attempts int) {
		t.Helper()
		var parents, children int
		err := store.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM routeforge_requests WHERE request_id=$1), (SELECT count(*) FROM routeforge_provider_attempts WHERE request_id=$1)", id).Scan(&parents, &children)
		if err != nil || parents != requests || children != attempts {
			t.Fatal("request/attempt atomicity violated")
		}
	}
	assertRows(0, 0)
	record.Attempts[1].TTFCUS = nil
	canceled, stop := context.WithCancel(ctx)
	stop()
	if err := store.Write(canceled, record); err == nil {
		t.Fatal("canceled write succeeded")
	}
	assertRows(0, 0)
	if err := store.Write(ctx, record); err != nil {
		t.Fatal("valid fallback chain failed")
	}
	assertRows(1, 2)
	if err := store.Write(ctx, record); err == nil {
		t.Fatal("duplicate request was accepted")
	}
	assertRows(1, 2)
	var unavailable bool
	if err := store.pool.QueryRow(ctx, "SELECT bool_and(ttfc_us IS NULL AND input_tokens IS NULL AND output_tokens IS NULL AND total_tokens IS NULL AND estimated_cost_micro_usd IS NULL) FROM routeforge_provider_attempts WHERE request_id=$1", id).Scan(&unavailable); err != nil || !unavailable {
		t.Fatal("nullable metadata changed")
	}
}
