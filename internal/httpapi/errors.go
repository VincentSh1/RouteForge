package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/VincentSh1/RouteForge/internal/gateway"
	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider"
)

func writeCompletionError(w http.ResponseWriter, err error) {
	var roleErr *gateway.UnsupportedRoleError
	switch {
	case errors.Is(err, gateway.ErrModelRequired):
		param := "model"
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", &param, "model_required")
	case errors.Is(err, gateway.ErrMessagesRequired):
		param := "messages"
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", &param, "messages_required")
	case errors.Is(err, gateway.ErrStreamingUnsupported):
		param := "stream"
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", &param, "unsupported_parameter")
	case errors.As(err, &roleErr):
		param := "messages"
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", &param, "unsupported_value")
	default:
		writeProviderError(w, err)
	}
}

func writeProviderError(w http.ResponseWriter, err error) {
	var providerErr *provider.Error
	if !errors.As(err, &providerErr) {
		writeError(w, http.StatusInternalServerError, "an internal error occurred", "server_error", nil, "internal_error")
		return
	}

	switch providerErr.Kind {
	case provider.ErrorInvalidRequest:
		writeError(w, http.StatusBadRequest, "the provider rejected the request", "invalid_request_error", nil, "provider_invalid_request")
	case provider.ErrorRateLimited:
		writeError(w, http.StatusTooManyRequests, "the provider rate limit was exceeded", "rate_limit_error", nil, "provider_rate_limited")
	case provider.ErrorTimeout:
		writeError(w, http.StatusGatewayTimeout, "the provider timed out", "server_error", nil, "provider_timeout")
	case provider.ErrorUnavailable:
		writeError(w, http.StatusServiceUnavailable, "the provider is unavailable", "server_error", nil, "provider_unavailable")
	default:
		writeError(w, http.StatusBadGateway, "the provider request failed", "server_error", nil, "provider_error")
	}
}

func writeError(w http.ResponseWriter, status int, message, errorType string, param *string, code string) {
	writeJSON(w, status, openai.ErrorResponse{Error: openai.APIError{
		Message: message,
		Type:    errorType,
		Param:   param,
		Code:    code,
	}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
