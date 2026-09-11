package adminapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/persistence"
)

type fakeReader struct {
	list   func(context.Context, persistence.ListQuery) ([]persistence.RequestRecord, error)
	detail func(context.Context, string) (persistence.RequestRecord, error)
}

func (f fakeReader) List(ctx context.Context, q persistence.ListQuery) ([]persistence.RequestRecord, error) {
	if f.list == nil {
		return nil, nil
	}
	return f.list(ctx, q)
}
func (f fakeReader) Detail(ctx context.Context, id string) (persistence.RequestRecord, error) {
	if f.detail == nil {
		return persistence.RequestRecord{}, persistence.ErrNotFound
	}
	return f.detail(ctx, id)
}
func testID(value byte) string {
	var raw [16]byte
	raw[15] = value
	return "rfreq_" + base64.RawURLEncoding.EncodeToString(raw[:])
}
func get(handler http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestEmptyListHasArrayAndNullCursor(t *testing.T) {
	handler := NewHandler(fakeReader{list: func(ctx context.Context, q persistence.ListQuery) ([]persistence.RequestRecord, error) {
		if q.Limit != 50 {
			t.Fatal("wrong default page size")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > persistence.QueryTimeout {
			t.Fatal("query is unbounded")
		}
		return nil, nil
	}})
	w := get(handler, "/admin/v1/requests")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"requests":[],"next_cursor":null}` {
		t.Fatalf("unexpected list: %d %s", w.Code, w.Body)
	}
	if w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("unsafe HTTP headers")
	}
}

func TestListPaginationPreservesOrderAndBoundary(t *testing.T) {
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 123000, time.UTC)
	records := []persistence.RequestRecord{{RequestID: testID(3), StartedAt: stamp}, {RequestID: testID(2), StartedAt: stamp}, {RequestID: testID(1), StartedAt: stamp.Add(-time.Second)}}
	call := 0
	handler := NewHandler(fakeReader{list: func(ctx context.Context, q persistence.ListQuery) ([]persistence.RequestRecord, error) {
		call++
		if q.Limit != 2 {
			t.Fatal("limit not forwarded")
		}
		if call == 1 {
			return records, nil
		}
		if q.Before == nil || q.Before.RequestID != records[1].RequestID || !q.Before.StartedAt.Equal(stamp) {
			t.Fatal("incorrect keyset boundary")
		}
		return records[2:], nil
	}})
	w := get(handler, "/admin/v1/requests?limit=2")
	var page struct {
		Requests   []requestSummary `json:"requests"`
		NextCursor *string          `json:"next_cursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Requests) != 2 || page.Requests[0].RequestID != records[0].RequestID || page.NextCursor == nil {
		t.Fatal("incorrect first page")
	}
	w = get(handler, "/admin/v1/requests?limit=2&cursor="+url.QueryEscape(*page.NextCursor))
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Requests) != 1 || page.Requests[0].RequestID != records[2].RequestID || page.NextCursor != nil {
		t.Fatal("incorrect final page")
	}
	if strings.Contains(w.Body.String(), `"attempts"`) {
		t.Fatal("list includes attempts")
	}
}

func TestFiltersAndLimitValidation(t *testing.T) {
	q, err := parseQuery("limit=100&provider=anthropic&routing_policy=cost_latency&outcome=timeout&streaming=false&cache_hit=true&started_after=2026-01-01T00:00:00Z&started_before=2026-02-01T00:00:00Z")
	if err != nil || q.Limit != 100 || q.Provider != "anthropic" || q.RoutingPolicy != "cost_latency" || q.Outcome != "timeout" || q.Streaming == nil || *q.Streaming || q.CacheHit == nil || !*q.CacheHit || q.StartedAfter == nil || q.StartedBefore == nil {
		t.Fatal("filter parsing failed")
	}
	for _, raw := range []string{"limit=0", "limit=-1", "limit=101", "limit=1.5", "limit=abc", "limit=1&limit=2", "limit=", "cursor=bad", "provider=other", "routing_policy=balanced", "outcome=raw-error", "streaming=1", "cache_hit=yes", "started_after=invalid", "started_after=2026-02-01T00:00:00Z&started_before=2026-01-01T00:00:00Z", "sort=request_id", "limit=%zz", strings.Repeat("x", 4097)} {
		t.Run(raw[:min(len(raw), 60)], func(t *testing.T) {
			reader := fakeReader{list: func(context.Context, persistence.ListQuery) ([]persistence.RequestRecord, error) {
				t.Fatal("invalid query reached database")
				return nil, nil
			}}
			w := get(NewHandler(reader), "/admin/v1/requests?"+raw)
			if w.Code != 400 {
				t.Fatalf("status %d", w.Code)
			}
		})
	}
}

func TestCursorRejectsInvalidStructures(t *testing.T) {
	position := persistence.Position{StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), RequestID: testID(1)}
	encoded := encodeCursor(position)
	decoded, err := decodeCursor(encoded)
	if err != nil || !reflect.DeepEqual(*decoded, position) {
		t.Fatal("valid cursor rejected")
	}
	for _, raw := range []string{`{}`, `{"started_at":"2026-01-01T00:00:00Z","request_id":"bad"}`, `{"sql":"SELECT"}`, `null`, `{"started_at":"0001-01-01T00:00:00Z","request_id":"` + testID(1) + `"}`, `{"started_at":"2026-01-01T00:00:00.000000001Z","request_id":"` + testID(1) + `"}`} {
		if _, err := decodeCursor(base64.RawURLEncoding.EncodeToString([]byte(raw))); err == nil {
			t.Fatal("invalid cursor accepted")
		}
	}
	if _, err := decodeCursor(strings.Repeat("A", 257)); err == nil {
		t.Fatal("oversized cursor accepted")
	}
}

func TestTimeFiltersRespectDatabasePrecision(t *testing.T) {
	if _, err := parseQuery("started_before=2026-01-01T00:00:00.000000001Z"); err == nil {
		t.Fatal("sub-microsecond boundary would be silently truncated by PostgreSQL")
	}
	if _, err := parseQuery("started_before=2026-01-01T00:00:00.000001Z"); err != nil {
		t.Fatal("valid microsecond boundary rejected")
	}
}

func TestDetailPreservesAvailableZeroAndIntegerValues(t *testing.T) {
	zero, cost, ttfc := uint64(0), uint64(123456), int64(42)
	record := persistence.RequestRecord{RequestID: testID(1), AttemptCount: 1,
		Attempts: []persistence.AttemptRecord{{AttemptNumber: 1, TTFCUS: &ttfc,
			InputTokens: &zero, OutputTokens: &zero, TotalTokens: &zero, EstimatedCostMicroUSD: &cost}}}
	w := get(NewHandler(fakeReader{detail: func(context.Context, string) (persistence.RequestRecord, error) {
		return record, nil
	}}), "/admin/v1/requests/"+record.RequestID)
	var result requestDetail
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	a := result.Attempts[0]
	if a.TTFCUS == nil || *a.TTFCUS != ttfc || a.InputTokens == nil || *a.InputTokens != 0 ||
		a.OutputTokens == nil || *a.OutputTokens != 0 || a.TotalTokens == nil || *a.TotalTokens != 0 ||
		a.EstimatedCostMicroUSD == nil || *a.EstimatedCostMicroUSD != cost {
		t.Fatal("available operational values changed")
	}
}

func TestAdminServerIsIsolatedAndBounded(t *testing.T) {
	server := NewServer("127.0.0.1:8081", nil, nil)
	if server.ReadHeaderTimeout <= 0 || server.WriteTimeout <= persistence.QueryTimeout || server.MaxHeaderBytes > 8192 {
		t.Fatal("unbounded admin server")
	}
	if get(server.Handler, "/admin/v1/health").Code != 200 || get(server.Handler, "/v1/chat/completions").Code != 404 {
		t.Fatal("admin listener must serve only admin endpoints")
	}
	if get(server.Handler, "/admin/v1/requests").Code != 503 {
		t.Fatal("missing reader must be unavailable")
	}
}

func TestDetailPreservesAttemptsNullsAndCacheHit(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(map[bool]string{false: "fallback", true: "cache hit"}[cached], func(t *testing.T) {
			record := persistence.RequestRecord{RequestID: testID(1), Outcome: "success", CacheHit: cached}
			if !cached {
				record.AttemptCount = 2
				record.FallbackCount = 1
				record.Attempts = []persistence.AttemptRecord{{AttemptNumber: 1, Provider: "openai", Outcome: "timeout"}, {AttemptNumber: 2, Provider: "anthropic", Outcome: "success", Fallback: true}}
			}
			handler := NewHandler(fakeReader{detail: func(ctx context.Context, id string) (persistence.RequestRecord, error) {
				if id != record.RequestID {
					t.Fatal("wrong ID")
				}
				return record, nil
			}})
			w := get(handler, "/admin/v1/requests/"+record.RequestID)
			var result requestDetail
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || result.CacheHit != cached || len(result.Attempts) != record.AttemptCount {
				t.Fatal("incorrect detail")
			}
			if cached {
				if !strings.Contains(w.Body.String(), `"attempts":[]`) {
					t.Fatal("empty attempts must be array")
				}
			} else {
				if result.Attempts[0].AttemptNumber != 1 || result.Attempts[1].AttemptNumber != 2 || !result.Attempts[1].Fallback {
					t.Fatal("attempt order changed")
				}
				for _, field := range []string{"ttfc_us", "input_tokens", "output_tokens", "total_tokens", "estimated_cost_micro_usd"} {
					if !strings.Contains(w.Body.String(), `"`+field+`":null`) {
						t.Fatal("unavailable value was fabricated")
					}
				}
			}
			assertPrivacy(t, w.Body.Bytes())
		})
	}
}

func assertPrivacy(t *testing.T, data []byte) {
	t.Helper()
	var object any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{}
	for _, key := range strings.Fields("request_id started_at completed_at routing_policy streaming logical_model initial_provider final_provider outcome attempt_count fallback_count request_duration_us cache_hit attempts attempt_number provider resolved_provider_model fallback duration_us ttfc_us input_tokens output_tokens total_tokens estimated_cost_micro_usd requests next_cursor") {
		allowed[key] = true
	}
	var walk func(any)
	walk = func(value any) {
		switch item := value.(type) {
		case map[string]any:
			for key, child := range item {
				if !allowed[key] {
					t.Fatalf("unapproved API field %s", key)
				}
				walk(child)
			}
		case []any:
			for _, child := range item {
				walk(child)
			}
		}
	}
	walk(object)
}

func TestLookupErrorsAreSanitizedAndMethodsReadOnly(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
	}{{"missing", persistence.ErrNotFound, 404}, {"database failure", errors.New("private database connection detail"), 503}} {
		h := NewHandler(fakeReader{detail: func(context.Context, string) (persistence.RequestRecord, error) {
			return persistence.RequestRecord{}, test.err
		}})
		w := get(h, "/admin/v1/requests/"+testID(1))
		if w.Code != test.status || strings.Contains(w.Body.String(), "private") {
			t.Fatal("unsafe lookup error")
		}
	}
	h := NewHandler(fakeReader{list: func(context.Context, persistence.ListQuery) ([]persistence.RequestRecord, error) {
		return nil, errors.New("private database detail")
	}})
	w := get(h, "/admin/v1/requests")
	if w.Code != 503 || strings.Contains(w.Body.String(), "private") {
		t.Fatal("unsafe list error")
	}
	w = get(h, "/admin/v1/requests/invalid")
	if w.Code != 400 {
		t.Fatal("invalid ID accepted")
	}
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/admin/v1/requests", nil))
		if w.Code != 405 || w.Header().Get("Allow") != "GET" {
			t.Fatal("write method accepted")
		}
	}
}
