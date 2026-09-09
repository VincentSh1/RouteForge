package postgres

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/persistence"
)

func TestListSQLUsesParametersAndStableKeyset(t *testing.T) {
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	yes, no := true, false
	query := persistence.ListQuery{Limit: 2, Before: &persistence.Position{StartedAt: stamp, RequestID: "synthetic-id"}, Provider: "provider' OR true --", RoutingPolicy: "latency", Outcome: "success", Streaming: &no, CacheHit: &yes, StartedAfter: &stamp, StartedBefore: &stamp}
	sql, args, err := listSQL(query)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"(started_at, request_id) < ($1, $2)", "(initial_provider = $3 OR final_provider = $3)", "ORDER BY started_at DESC, request_id DESC LIMIT $10"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("missing query structure %s", required)
		}
	}
	if strings.Contains(sql, query.Provider) || strings.Contains(sql, "OFFSET") || strings.Contains(sql, "SELECT *") {
		t.Fatal("unbounded or interpolated query")
	}
	want := []any{stamp, "synthetic-id", query.Provider, "latency", "success", false, true, stamp, stamp, 3}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("incorrect parameter binding: %#v", args)
	}
}

func TestListSQLRequiresBoundedLimit(t *testing.T) {
	for _, limit := range []int{-1, 0, 101} {
		if _, _, err := listSQL(persistence.ListQuery{Limit: limit}); err == nil {
			t.Fatal("unbounded query accepted")
		}
	}
	sql, args, err := listSQL(persistence.ListQuery{Limit: 100})
	if err != nil || !strings.HasSuffix(sql, "LIMIT $1") || args[0] != 101 {
		t.Fatal("missing bounded lookahead")
	}
}

func TestPaginationMigrationMatchesOrdering(t *testing.T) {
	data, err := migrationFiles.ReadFile("migrations/0003_history_pagination.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "(started_at DESC, request_id DESC)") {
		t.Fatal("missing pagination index")
	}
}
