package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/config"
)

func TestMetricsServerUsesDedicatedAddressAndTimeouts(t *testing.T) {
	cfg := config.Config{
		MetricsAddr: "127.0.0.1:9090",
		ReadTimeout: time.Second, WriteTimeout: 2 * time.Second, IdleTimeout: 3 * time.Second,
	}
	server := NewMetricsServer(cfg, http.NotFoundHandler())
	if server.Addr != cfg.MetricsAddr || server.ReadHeaderTimeout != cfg.ReadTimeout || server.ReadTimeout != cfg.ReadTimeout ||
		server.WriteTimeout != cfg.WriteTimeout || server.IdleTimeout != cfg.IdleTimeout {
		t.Fatalf("metrics server = %+v", server)
	}
}
