package adminapi

import (
	"github.com/VincentSh1/RouteForge/internal/gateway"
	"github.com/VincentSh1/RouteForge/internal/provider/mock"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRoutingWritesRequireSessionAndOrigin(t *testing.T) {
	service := gateway.New(&mock.Provider{}, nil)
	store, _, secret := authFixture()
	handler := authHandler(routingHandler(service), store)
	cookie := loginCookie(t, handler, secret)
	path := "/admin/v1/routing/config"
	body := `{"policy":"cost_latency","exploration_interval":7,"max_latency_over_fastest_percent":0}`
	if authRequest(handler, "PUT", path, body).Code != 401 {
		t.Fatal("anonymous write allowed")
	}
	if authRequest(authHandler(routingHandler(service), nil), "PUT", path, body).Code != 403 {
		t.Fatal("disabled authentication permits write")
	}
	for _, origin := range []string{"", "http://untrusted.invalid"} {
		r := httptest.NewRequest("PUT", path, strings.NewReader(body))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("origin accepted")
		}
	}
	for _, header := range []struct{ key, value string }{{"Content-Type", "text/plain"}, {"Sec-Fetch-Site", "cross-site"}} {
		r := httptest.NewRequest("PUT", path, strings.NewReader(body))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://127.0.0.1:3001")
		r.Header.Set(header.key, header.value)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("unsafe write accepted")
		}
	}
	if authRequest(handler, "PUT", path, body, cookie).Code != 200 {
		t.Fatal("valid update failed")
	}
	if service.RoutingSettings().Policy != "cost_latency" {
		t.Fatal("not updated")
	}
	before := service.RoutingSettings()
	for _, invalid := range []string{
		strings.Repeat(" ", 1025) + body,
		`{"policy":"cost","policy":"latency","exploration_interval":7,"max_latency_over_fastest_percent":null}`,
		`{}`, `null`, `{"policy":"cost","exploration_interval":7,"extra":1}`, body + `{}`,
		strings.Replace(body, `"cost_latency"`, `"balanced"`, 1), strings.Replace(body, `:7`, `:0`, 1),
		strings.Replace(body, `:7`, `:1.5`, 1), strings.Replace(body, `:7`, `:1000001`, 1),
		strings.Replace(body, `percent":0`, `percent":null`, 1), strings.Replace(body, `percent":0`, `percent":-1`, 1),
		strings.Replace(body, `percent":0`, `percent":1000001`, 1),
	} {
		if authRequest(handler, "PUT", path, invalid, cookie).Code != 400 {
			t.Fatal("invalid update accepted")
		}
		if !reflect.DeepEqual(before, service.RoutingSettings()) {
			t.Fatal("invalid update mutated state")
		}
	}
	store.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if authRequest(handler, "PUT", path, body, cookie).Code != 401 {
		t.Fatal("expired session accepted")
	}
}
