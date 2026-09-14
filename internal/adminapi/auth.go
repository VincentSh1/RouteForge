package adminapi

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

const sessionCookie = "routeforge_admin_session"
const maxSessions = 128

// AuthConfig is supplied only by validated startup configuration, never requests.
type AuthConfig struct {
	Enabled bool
	Secret  string
	TTL     time.Duration
	Origin  string
}

type sessions struct {
	mu      sync.Mutex
	entries map[[32]byte]time.Time
	secret  [32]byte
	ttl     time.Duration
	origin  string
	secure  bool
	now     func() time.Time
	random  io.Reader
	window  time.Time
	logins  int
}

func newSessions(config AuthConfig) *sessions {
	return &sessions{entries: make(map[[32]byte]time.Time), secret: sha256.Sum256([]byte(config.Secret)), ttl: config.TTL,
		origin: config.Origin, secure: strings.HasPrefix(config.Origin, "https://"), now: time.Now, random: rand.Reader}
}

func cookieKey(r *http.Request) ([32]byte, bool) {
	cookies := r.CookiesNamed(sessionCookie)
	if len(cookies) != 1 || len(cookies[0].Value) != 43 {
		return [32]byte{}, false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(cookies[0].Value)
	if err != nil || len(raw) != 32 {
		return [32]byte{}, false
	}
	return sha256.Sum256(raw), true
}

func (s *sessions) prune(now time.Time) {
	for key, expiry := range s.entries {
		if !now.Before(expiry) {
			delete(s.entries, key)
		}
	}
}

func (s *sessions) valid(r *http.Request) bool {
	key, ok := cookieKey(r)
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.prune(now)
	expiry, found := s.entries[key]
	return found && now.Before(expiry)
}

func (s *sessions) cookie(value string, expiry time.Time, maxAge int) *http.Cookie {
	return &http.Cookie{Name: sessionCookie, Value: value, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: s.secure, MaxAge: maxAge, Expires: expiry}
}

func authFailure(w http.ResponseWriter) {
	writeError(w, 401, "unauthorized", "authentication required")
}

func (s *sessions) login(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	now := s.now()
	if !now.Before(s.window.Add(time.Minute)) {
		s.window = now
		s.logins = 0
	}
	limited := s.logins >= 20
	if !limited {
		s.logins++
	}
	s.mu.Unlock()
	if limited {
		w.Header().Set("Retry-After", "60")
		writeError(w, 429, "login_limited", "login temporarily unavailable")
		return
	}
	var input struct {
		Secret string `json:"secret"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&input)
	var extra any
	if err != nil || decoder.Decode(&extra) != io.EOF || len(input.Secret) > 256 {
		authFailure(w)
		return
	}
	provided := sha256.Sum256([]byte(input.Secret))
	if subtle.ConstantTimeCompare(provided[:], s.secret[:]) != 1 {
		authFailure(w)
		return
	}
	var raw [32]byte
	if _, err := io.ReadFull(s.random, raw[:]); err != nil {
		writeError(w, 503, "auth_unavailable", "authentication unavailable")
		return
	}
	key := sha256.Sum256(raw[:])
	s.mu.Lock()
	now = s.now()
	s.prune(now)
	old, hadOld := cookieKey(r)
	_, replacing := s.entries[old]
	_, collision := s.entries[key]
	if collision || (len(s.entries) >= maxSessions && !(hadOld && replacing)) {
		s.mu.Unlock()
		writeError(w, 503, "auth_unavailable", "authentication unavailable")
		return
	}
	if hadOld {
		delete(s.entries, old)
	}
	expiry := now.Add(s.ttl)
	s.entries[key] = expiry
	s.mu.Unlock()
	http.SetCookie(w, s.cookie(base64.RawURLEncoding.EncodeToString(raw[:]), expiry, int(s.ttl.Seconds())))
	writeJSON(w, 200, map[string]bool{"authenticated": true})
}

// Authenticate wraps the entire admin mux, including unknown paths and health.
// Only session status and login/logout are public; they reveal no operational data.
func Authenticate(next http.Handler, config AuthConfig) http.Handler {
	var store *sessions
	if config.Enabled {
		store = newSessions(config)
	}
	return authHandler(next, store)
}

func authHandler(next http.Handler, store *sessions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		path := r.URL.Path
		if path == "/admin/v1/auth/session" {
			if r.Method != http.MethodGet || r.URL.RawQuery != "" {
				writeError(w, 400, "invalid_request", "invalid session request")
				return
			}
			writeJSON(w, 200, struct {
				Enabled       bool `json:"enabled"`
				Authenticated bool `json:"authenticated"`
			}{store != nil, store == nil || store.valid(r)})
			return
		}
		if path == "/admin/v1/auth/login" || path == "/admin/v1/auth/logout" {
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", "POST")
				writeError(w, 405, "method_not_allowed", "method not allowed")
				return
			}
			if store == nil {
				writeError(w, 404, "not_found", "endpoint not found")
				return
			}
			media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			origins := r.Header.Values("Origin")
			if r.URL.RawQuery != "" || err != nil || media != "application/json" || len(origins) != 1 || origins[0] != store.origin || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				writeError(w, 403, "forbidden", "request not permitted")
				return
			}
			if path == "/admin/v1/auth/login" {
				store.login(w, r)
				return
			}
			if key, ok := cookieKey(r); ok {
				store.mu.Lock()
				delete(store.entries, key)
				store.mu.Unlock()
			}
			http.SetCookie(w, store.cookie("", time.Unix(1, 0), -1))
			writeJSON(w, 200, map[string]bool{"authenticated": false})
			return
		}
		if store != nil && !store.valid(r) {
			authFailure(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}
