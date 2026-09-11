package adminapi

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/VincentSh1/RouteForge/internal/gateway"
)

func TestOverviewReadOnlySerialization(t *testing.T) {
	calls := 0
	h := NewServer("", nil, func() Overview {
		calls++
		return Overview{OperationsSnapshot: gateway.OperationsSnapshot{Providers: []gateway.ProviderState{}, Routing: gateway.RoutingState{Policy: "deterministic", ProviderOrder: []string{}}}, Features: Features{Persistence: true}}
	}).Handler
	w := get(h, "/admin/v1/overview")
	var result map[string]json.RawMessage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result) != 4 || string(result["providers"]) != "[]" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("incorrect state response")
	}
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/admin/v1/overview", nil))
		if w.Code != 405 {
			t.Fatal("write accepted")
		}
	}
	if get(h, "/admin/v1/overview?secret=excluded").Code != 400 || calls != 1 {
		t.Fatal("invalid request evaluated state")
	}
	w = get(NewServer("", nil, nil).Handler, "/admin/v1/overview")
	if w.Code != 503 || w.Body.String() != "{\"error\":{\"code\":\"state_unavailable\",\"message\":\"operational state unavailable\"}}\n" {
		t.Fatal("unsafe missing state error")
	}
}
