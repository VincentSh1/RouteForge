package adminapi

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/VincentSh1/RouteForge/internal/gateway"
)

type routingControl interface {
	RoutingSettings() gateway.RoutingSettings
	UpdateRoutingSettings(gateway.RoutingSettings) (gateway.RoutingSettings, error)
}
type routingWriteAuthorized struct{}

func routingHandler(control routingControl) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.RawQuery != "" {
			writeError(w, 400, "invalid_request", "query parameters are not supported")
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, 200, control.RoutingSettings())
		case http.MethodPut:
			if r.Context().Value(routingWriteAuthorized{}) != true {
				writeError(w, 403, "forbidden", "request not permitted")
				return
			}
			// A replacement document requires all fields, including explicit null for
			// an unconfigured tolerance. Unknown settings cannot expand the write surface.
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
			fields, valid := routingDocument(decoder)
			if !valid {
				writeError(w, 400, "invalid_settings", "invalid routing settings")
				return
			}
			var settings gateway.RoutingSettings
			data, _ := json.Marshal(fields)
			if json.Unmarshal(data, &settings) != nil {
				writeError(w, 400, "invalid_settings", "invalid routing settings")
				return
			}
			result, err := control.UpdateRoutingSettings(settings)
			if err != nil {
				writeError(w, 400, "invalid_settings", "invalid routing settings")
				return
			}
			writeJSON(w, 200, result)
		default:
			w.Header().Set("Allow", "GET, PUT")
			writeError(w, 405, "method_not_allowed", "method not allowed")
		}
	})
}

func routingDocument(decoder *json.Decoder) (map[string]json.RawMessage, bool) {
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]json.RawMessage, 3)
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || fields[key] != nil {
			return nil, false
		}
		switch key {
		case "policy", "exploration_interval", "max_latency_over_fastest_percent":
		default:
			return nil, false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, false
		}
		fields[key] = value
	}
	end, err := decoder.Token()
	return fields, err == nil && end == json.Delim('}') && len(fields) == 3 && decoder.Decode(new(any)) == io.EOF
}
