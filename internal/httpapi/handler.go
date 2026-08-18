package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/VincentSh1/RouteForge/internal/gateway"
	"github.com/VincentSh1/RouteForge/internal/openai"
)

const maxRequestBodyBytes = 1 << 20 // 1 MiB

type completer interface {
	Complete(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error)
}

type Handler struct {
	completer completer
}

func NewHandler(completer completer) *Handler { return &Handler{completer: completer} }

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", h.health)
	mux.HandleFunc("/v1/chat/completions", h.chatCompletions)
	return mux
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error", nil, "method_not_allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) chatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error", nil, "method_not_allowed")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	defer r.Body.Close()

	var req openai.ChatCompletionRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&req); err != nil {
		message := "request body must contain valid JSON"
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			message = "request body is too large"
		}
		writeError(w, http.StatusBadRequest, message, "invalid_request_error", nil, "invalid_json")
		return
	}
	if err := ensureSingleJSONValue(decoder); err != nil {
		writeError(w, http.StatusBadRequest, "request body must contain a single JSON object", "invalid_request_error", nil, "invalid_json")
		return
	}

	response, err := h.completer.Complete(r.Context(), req)
	if err != nil {
		writeCompletionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func ensureSingleJSONValue(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values")
	}
	return err
}
