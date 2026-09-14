package config

import (
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

func loadAdminAuth(cfg *Config) error {
	cfg.AdminSessionTTL = 8 * time.Hour
	cfg.AdminOrigin = envOrDefault("ROUTEFORGE_ADMIN_ORIGIN", "http://127.0.0.1:3001")
	if raw := os.Getenv("ROUTEFORGE_ADMIN_SESSION_TTL"); raw != "" {
		ttl, err := time.ParseDuration(raw)
		if err != nil || ttl < time.Minute || ttl > 24*time.Hour || ttl%time.Second != 0 {
			return validationError("ROUTEFORGE_ADMIN_SESSION_TTL must be whole seconds between 1m and 24h")
		}
		cfg.AdminSessionTTL = ttl
	}
	if !cfg.AdminAuthEnabled {
		return nil
	}
	if !cfg.AdminEnabled {
		return validationError("admin authentication requires the admin listener")
	}
	cfg.AdminSecret = os.Getenv("ROUTEFORGE_ADMIN_SECRET")
	if len(cfg.AdminSecret) < 32 || len(cfg.AdminSecret) > 256 || strings.TrimSpace(cfg.AdminSecret) != cfg.AdminSecret {
		return validationError("ROUTEFORGE_ADMIN_SECRET must contain 32 to 256 bytes without surrounding whitespace")
	}
	origin, err := url.Parse(cfg.AdminOrigin)
	if err != nil || origin.User != nil || origin.Hostname() == "" || origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" || (origin.Scheme != "http" && origin.Scheme != "https") {
		return validationError("ROUTEFORGE_ADMIN_ORIGIN must be an explicit HTTP(S) origin without a path")
	}
	if origin.Scheme == "http" && origin.Hostname() != "localhost" {
		ip := net.ParseIP(origin.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return validationError("HTTP admin origins must be loopback; remote use requires HTTPS")
		}
	}
	if port := origin.Port(); port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return validationError("ROUTEFORGE_ADMIN_ORIGIN must use a valid port")
		}
	}
	return nil
}
