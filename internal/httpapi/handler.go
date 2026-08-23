package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/VincentSh1/RouteForge/internal/gateway"
	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider"
)

const maxRequestBodyBytes = 1 << 20 // 1 MiB

type completer interface {
	Complete(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error)
	Stream(context.Context, openai.ChatCompletionRequest, gateway.EmitFunc) error
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
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "content type must be application/json", "invalid_request_error", nil, "unsupported_media_type")
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
	if req.Stream {
		h.streamChatCompletions(w, r, req)
		return
	}

	response, err := h.completer.Complete(r.Context(), req)
	if err != nil {
		writeCompletionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) streamChatCompletions(w http.ResponseWriter, r *http.Request, req openai.ChatCompletionRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is unavailable", "server_error", nil, "streaming_unavailable")
		return
	}
	committed := false
	emit := func(chunk provider.StreamChunk) error {
		payload := openai.ChatCompletionChunk{
			ID: chunk.ID, Object: "chat.completion.chunk", Created: chunk.Created, Model: chunk.Model,
			Choices: []openai.ChunkChoice{{
				Index: 0,
				Delta: openai.ChunkDelta{Role: chunk.Role, Content: chunk.Content},
			}},
		}
		if chunk.FinishReason != "" {
			payload.Choices[0].FinishReason = &chunk.FinishReason
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if !committed {
			prepareSSE(w)
		}
		written, err := w.Write(append(append([]byte("data: "), data...), '\n', '\n'))
		if written > 0 {
			committed = true
		}
		if err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	if err := h.completer.Stream(r.Context(), req, emit); err != nil {
		if !committed {
			writeCompletionError(w, err)
		}
		return
	}
	if !committed {
		prepareSSE(w)
	}
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
}

func prepareSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Del("Content-Length")
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
