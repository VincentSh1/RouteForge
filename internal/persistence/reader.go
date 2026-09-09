package persistence

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"
)

const (
	DefaultPageSize   = 50
	MaxPageSize       = 100
	MaxDetailAttempts = 100
	QueryTimeout      = 2 * time.Second
)

var ErrNotFound = errors.New("request not found")

type Position struct {
	StartedAt time.Time `json:"started_at"`
	RequestID string    `json:"request_id"`
}

type ListQuery struct {
	Limit         int
	Before        *Position
	Provider      string
	RoutingPolicy string
	Outcome       string
	Streaming     *bool
	CacheHit      *bool
	StartedAfter  *time.Time
	StartedBefore *time.Time
}

// Reader exposes only durable operational metadata. List returns up to Limit+1
// summaries so the HTTP layer can determine whether another page exists.
type Reader interface {
	List(context.Context, ListQuery) ([]RequestRecord, error)
	Detail(context.Context, string) (RequestRecord, error)
}

func ValidRequestID(id string) bool {
	if len(id) != len(requestIDPrefix)+22 || !strings.HasPrefix(id, requestIDPrefix) {
		return false
	}
	raw := strings.TrimPrefix(id, requestIDPrefix)
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	return err == nil && len(decoded) == 16 && base64.RawURLEncoding.EncodeToString(decoded) == raw
}
