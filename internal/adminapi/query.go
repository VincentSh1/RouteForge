package adminapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/VincentSh1/RouteForge/internal/persistence"
)

func encodeCursor(position persistence.Position) string {
	data, _ := json.Marshal(position)
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeCursor(raw string) (*persistence.Position, error) {
	if len(raw) > 256 {
		return nil, errors.New("invalid cursor")
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil {
		return nil, errors.New("invalid cursor")
	}
	var position persistence.Position
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&position) != nil || !persistence.ValidRequestID(position.RequestID) || position.StartedAt.IsZero() ||
		position.StartedAt.Year() < 1 || position.StartedAt.Year() > 9999 || position.StartedAt.Nanosecond()%1000 != 0 || encodeCursor(position) != raw {
		return nil, errors.New("invalid cursor")
	}
	return &position, nil
}

func parseQuery(raw string) (persistence.ListQuery, error) {
	query := persistence.ListQuery{Limit: persistence.DefaultPageSize}
	if len(raw) > 4096 {
		return query, errors.New("invalid query")
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return query, errors.New("invalid query")
	}
	for key, items := range values {
		if len(items) != 1 || items[0] == "" {
			return query, errors.New("invalid query")
		}
		value := items[0]
		switch key {
		case "limit":
			for _, digit := range value {
				if digit < '0' || digit > '9' {
					return query, errors.New("invalid limit")
				}
			}
			limit, err := strconv.Atoi(value)
			if err != nil || limit < 1 || limit > persistence.MaxPageSize {
				return query, errors.New("invalid limit")
			}
			query.Limit = limit
		case "cursor":
			position, err := decodeCursor(value)
			if err != nil {
				return query, err
			}
			query.Before = position
		case "provider":
			if value != "mock" && value != "openai" && value != "anthropic" {
				return query, errors.New("invalid provider")
			}
			query.Provider = value
		case "routing_policy":
			if value != "deterministic" && value != "latency" && value != "cost" && value != "cost_latency" {
				return query, errors.New("invalid policy")
			}
			query.RoutingPolicy = value
		case "outcome":
			switch value {
			case "success", "cancellation", "timeout", "unavailable", "rate_limited", "invalid_request", "internal", "other_failure":
			default:
				return query, errors.New("invalid outcome")
			}
			query.Outcome = value
		case "streaming", "cache_hit":
			if value != "true" && value != "false" {
				return query, errors.New("invalid boolean")
			}
			parsed := value == "true"
			if key == "streaming" {
				query.Streaming = &parsed
			} else {
				query.CacheHit = &parsed
			}
		case "started_after", "started_before":
			stamp, err := time.Parse(time.RFC3339Nano, value)
			if err != nil || stamp.Year() < 1 || stamp.Nanosecond()%1000 != 0 {
				return query, errors.New("invalid timestamp")
			}
			stamp = stamp.UTC()
			if key == "started_after" {
				query.StartedAfter = &stamp
			} else {
				query.StartedBefore = &stamp
			}
		default:
			return query, errors.New("unknown query parameter")
		}
	}
	if query.StartedAfter != nil && query.StartedBefore != nil && !query.StartedAfter.Before(*query.StartedBefore) {
		return query, errors.New("invalid time range")
	}
	return query, nil
}
