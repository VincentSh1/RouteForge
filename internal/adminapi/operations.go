package adminapi

import (
	"net/http"

	"github.com/VincentSh1/RouteForge/internal/gateway"
)

type Features struct {
	Cache       bool `json:"cache"`
	Persistence bool `json:"persistence"`
	Metrics     bool `json:"metrics"`
	Tracing     bool `json:"tracing"`
}

type Overview struct {
	gateway.OperationsSnapshot
	Features Features `json:"features"`
}

func operationsHandler(snapshot func() Overview) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeError(w, 405, "method_not_allowed", "method not allowed")
			return
		}
		if r.URL.RawQuery != "" {
			writeError(w, 400, "invalid_query", "query parameters are not supported")
			return
		}
		if snapshot == nil {
			writeError(w, 503, "state_unavailable", "operational state unavailable")
			return
		}
		writeJSON(w, 200, snapshot())
	})
}
