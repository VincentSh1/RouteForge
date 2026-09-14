package adminapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/gateway"
	"github.com/VincentSh1/RouteForge/internal/httpapi"
	"github.com/VincentSh1/RouteForge/internal/model"
	"github.com/VincentSh1/RouteForge/internal/provider/mock"
)

func authFixture() (*sessions, http.Handler, string) {
	secret := strings.Repeat("test-only-", 4)
	s := newSessions(AuthConfig{Enabled: true, Secret: secret, TTL: time.Hour, Origin: "http://127.0.0.1:3001"})
	return s, authHandler(NewServer("", nil, func() Overview { return Overview{} }).Handler, s), secret
}

func authRequest(h http.Handler, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Origin", "http://127.0.0.1:3001")
	r.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func loginCookie(t *testing.T, h http.Handler, secret string) *http.Cookie {
	t.Helper()
	data, _ := json.Marshal(map[string]string{"secret": secret})
	w := authRequest(h, "POST", "/admin/v1/auth/login", string(data))
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatal("login failed")
	}
	return w.Result().Cookies()[0]
}

func TestAuthenticationLifecycleAndBoundaries(t *testing.T) {
	s, h, secret := authFixture()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	paths := []string{"/admin/v1/requests", "/admin/v1/requests/rfreq_AAAAAAAAAAAAAAAAAAAAAA", "/admin/v1/overview", "/admin/v1/benchmarks", "/admin/v1/benchmarks/stable", "/admin/v1/health", "/admin/v1/unknown"}
	for _, path := range paths {
		if get(h, path).Code != 401 {
			t.Fatal("unprotected endpoint")
		}
	}
	cookie := loginCookie(t, h, secret)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Secure || cookie.Domain != "" || cookie.Path != "/" || cookie.MaxAge != 3600 || len(cookie.Value) != 43 {
		t.Fatal("unsafe cookie properties")
	}
	if authRequest(h, "GET", "/admin/v1/overview", "", cookie).Code != 200 {
		t.Fatal("session rejected")
	}
	keyRequest := httptest.NewRequest("GET", "/", nil)
	keyRequest.AddCookie(cookie)
	key, ok := cookieKey(keyRequest)
	if !ok || len(s.entries) != 1 || s.entries[key] != now.Add(time.Hour) {
		t.Fatal("session not registered")
	}
	now = now.Add(time.Hour)
	if authRequest(h, "GET", "/admin/v1/overview", "", cookie).Code != 401 || len(s.entries) != 0 {
		t.Fatal("expired session retained")
	}
	cookie = loginCookie(t, h, secret)
	w := authRequest(h, "POST", "/admin/v1/auth/logout", "{}", cookie)
	if w.Code != 200 || w.Result().Cookies()[0].MaxAge != -1 || authRequest(h, "GET", "/admin/v1/overview", "", cookie).Code != 401 {
		t.Fatal("logout failed")
	}
	if strings.Contains(w.Body.String(), secret) || strings.Contains(w.Body.String(), cookie.Value) {
		t.Fatal("sensitive response")
	}
	// The inference handler is separate and never wrapped in admin sessions.
	inference := httpapi.NewHandler(gateway.New(&mock.Provider{}, model.New(nil))).Routes()
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"mock-model","messages":[{"role":"user","content":"synthetic"}]}`))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	inference.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("inference requires authentication")
	}
}

func TestAuthenticationDisabled(t *testing.T) {
	h := Authenticate(NewServer("", nil, func() Overview { return Overview{} }).Handler, AuthConfig{})
	if get(h, "/admin/v1/overview").Code != 200 || get(h, "/admin/v1/auth/session").Body.String() != "{\"enabled\":false,\"authenticated\":true}\n" {
		t.Fatal("disabled behavior changed")
	}
	if authRequest(h, "POST", "/admin/v1/auth/login", `{}`).Code != 404 {
		t.Fatal("disabled login accepted")
	}
}

func TestAuthenticationFailuresAndCSRF(t *testing.T) {
	_, h, _ := authFixture()
	for _, body := range []string{`{}`, `{"secret":"wrong"}`, `{"secret":1}`, `{"secret":"wrong","extra":"private"}`, `{"secret":"wrong"}{}`, strings.Repeat("x", 2048)} {
		w := authRequest(h, "POST", "/admin/v1/auth/login", body)
		if w.Code != 401 || w.Body.String() != "{\"error\":{\"code\":\"unauthorized\",\"message\":\"authentication required\"}}\n" || len(w.Result().Cookies()) != 0 {
			t.Fatal("inconsistent login failure")
		}
	}
	for _, path := range []string{"login", "logout"} {
		for _, headers := range []map[string]string{
			{"Origin": "http://127.0.0.1:3001", "Content-Type": "text/plain"},
			{"Origin": "http://127.0.0.1:3001", "Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"},
		} {
			r := httptest.NewRequest("POST", "/admin/v1/auth/"+path, strings.NewReader(`{}`))
			for key, value := range headers {
				r.Header.Set(key, value)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatal("unsafe request metadata accepted")
			}
		}
		for _, origin := range []string{"", "null", "https://untrusted.invalid", "http://127.0.0.1:3001.evil.invalid"} {
			r := httptest.NewRequest("POST", "/admin/v1/auth/"+path, strings.NewReader(`{}`))
			r.Header.Set("Origin", origin)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatal("cross-origin auth request accepted")
			}
		}
		if get(h, "/admin/v1/auth/"+path).Code != 405 {
			t.Fatal("GET changes session")
		}
	}
	for _, value := range []string{"", "bad", strings.Repeat("a", 1000), strings.Repeat("!", 43)} {
		if authRequest(h, "GET", "/admin/v1/overview", "", &http.Cookie{Name: sessionCookie, Value: value}).Code != 401 {
			t.Fatal("malformed cookie accepted")
		}
	}
	_, other, secret := authFixture()
	cookie := loginCookie(t, other, secret)
	if authRequest(other, "GET", "/admin/v1/overview", "", cookie, cookie).Code != 401 {
		t.Fatal("duplicate cookie accepted")
	}
	if authRequest(h, "GET", "/admin/v1/overview", "", cookie).Code != 401 {
		t.Fatal("sessions survive server restart")
	}
}

type brokenRandom struct{}

func (brokenRandom) Read([]byte) (int, error) { return 0, errors.New("private entropy failure") }

func TestSessionBoundsRotationAndSecureCookie(t *testing.T) {
	s, h, secret := authFixture()
	s.secure = true
	cookie := loginCookie(t, h, secret)
	if !cookie.Secure {
		t.Fatal("HTTPS cookie not secure")
	}
	data, _ := json.Marshal(map[string]string{"secret": secret})
	w := authRequest(h, "POST", "/admin/v1/auth/login", string(data), cookie)
	if w.Code != 200 || w.Result().Cookies()[0].Value == cookie.Value || authRequest(h, "GET", "/admin/v1/overview", "", cookie).Code != 401 {
		t.Fatal("session not rotated")
	}
	for len(s.entries) < maxSessions {
		var key [32]byte
		key[0] = byte(len(s.entries))
		s.entries[key] = s.now().Add(time.Hour)
	}
	if authRequest(h, "POST", "/admin/v1/auth/login", string(data)).Code != 503 || len(s.entries) != maxSessions {
		t.Fatal("unbounded sessions")
	}
	s.random = brokenRandom{}
	w = authRequest(h, "POST", "/admin/v1/auth/login", string(data))
	if w.Code != 503 || strings.Contains(w.Body.String(), "entropy") {
		t.Fatal("entropy error leaked")
	}
}

func TestConcurrentAuthenticationAndThrottle(t *testing.T) {
	s, h, secret := authFixture()
	cookie := loginCookie(t, h, secret)
	data, _ := json.Marshal(map[string]string{"secret": secret})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = authRequest(h, "POST", "/admin/v1/auth/login", string(data))
			for range 20 {
				_ = authRequest(h, "GET", "/admin/v1/overview", "", cookie)
			}
			_ = authRequest(h, "POST", "/admin/v1/auth/logout", "{}", cookie)
		}()
	}
	wg.Wait()
	for range 25 {
		_ = authRequest(h, "POST", "/admin/v1/auth/login", `{}`)
	}
	if authRequest(h, "POST", "/admin/v1/auth/login", `{}`).Code != 429 || s.logins != 20 {
		t.Fatal("login throttle is unbounded")
	}
}
