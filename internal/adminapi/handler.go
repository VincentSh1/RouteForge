// Package adminapi serves only operational metadata on a dedicated local listener.
package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/VincentSh1/RouteForge/internal/persistence"
)

func NewServer(addr string, reader persistence.Reader, snapshot func() Overview) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/admin/v1/overview", operationsHandler(snapshot))
	mux.Handle("/", NewHandler(reader))
	return &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
}

func NewHandler(reader persistence.Reader) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/v1/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("/admin/v1/requests", func(w http.ResponseWriter, r *http.Request) {
		query, err := parseQuery(r.URL.RawQuery)
		if err != nil {
			writeError(w, 400, "invalid_query", "invalid history query")
			return
		}
		if reader == nil {
			writeError(w, 503, "history_unavailable", "history unavailable")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), persistence.QueryTimeout)
		defer cancel()
		records, err := reader.List(ctx, query)
		if err != nil {
			writeError(w, 503, "history_unavailable", "history unavailable")
			return
		}
		var next *string
		if len(records) > query.Limit {
			records = records[:query.Limit]
			last := records[len(records)-1]
			cursor := encodeCursor(persistence.Position{StartedAt: last.StartedAt.UTC(), RequestID: last.RequestID})
			next = &cursor
		}
		items := make([]requestSummary, 0, len(records))
		for _, record := range records {
			items = append(items, summary(record))
		}
		writeJSON(w, 200, struct {
			Requests   []requestSummary `json:"requests"`
			NextCursor *string          `json:"next_cursor"`
		}{items, next})
	})
	mux.HandleFunc("/admin/v1/requests/{request_id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("request_id")
		if !persistence.ValidRequestID(id) || r.URL.RawQuery != "" {
			writeError(w, 400, "invalid_request", "invalid request ID or query")
			return
		}
		if reader == nil {
			writeError(w, 503, "history_unavailable", "history unavailable")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), persistence.QueryTimeout)
		defer cancel()
		record, err := reader.Detail(ctx, id)
		if errors.Is(err, persistence.ErrNotFound) {
			writeError(w, 404, "not_found", "request not found")
			return
		}
		if err != nil {
			writeError(w, 503, "history_unavailable", "history unavailable")
			return
		}
		writeJSON(w, 200, detail(record))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { writeError(w, 404, "not_found", "endpoint not found") })
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeError(w, 405, "method_not_allowed", "method not allowed")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{Error: struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{code, message}})
}
