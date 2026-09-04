package observability

import (
	"os"
	"strings"
	"testing"
)

func TestComposeObservabilityTopology(t *testing.T) {
	compose := readDeploymentFile(t, "../../compose.yml")
	for _, required := range []string{
		"  routeforge:\n",
		"  prometheus:\n",
		"  grafana:\n",
		"ROUTEFORGE_PROVIDER: mock",
		"ROUTEFORGE_METRICS_ADDR: 0.0.0.0:9090",
		"ROUTEFORGE_OTEL_ENABLED: \"false\"",
		"ROUTEFORGE_PRICE_MOCK_INPUT_USD_PER_MILLION: \"2.000000\"",
		"ROUTEFORGE_PRICE_MOCK_OUTPUT_USD_PER_MILLION: \"8.000000\"",
		"image: prom/prometheus:v3.13.2",
		"image: grafana/grafana:13.2.1",
		"127.0.0.1:8080:8080",
		"127.0.0.1:9091:9090",
		"127.0.0.1:3000:3000",
		"http://127.0.0.1:8080/health",
		"http://127.0.0.1:9090/-/ready",
		"http://127.0.0.1:3000/api/health",
		"GF_AUTH_ANONYMOUS_ORG_ROLE: Viewer",
		"prometheus-data:/prometheus",
		"grafana-data:/var/lib/grafana",
		"prometheus.compose.yml:/etc/prometheus/prometheus.yml:ro",
		"alerts.yml:/etc/prometheus/alerts.yml:ro",
		"grafana/provisioning:/etc/grafana/provisioning:ro",
		"grafana/dashboards:/var/lib/grafana/dashboards:ro",
		"condition: service_healthy",
	} {
		if !strings.Contains(compose, required) {
			t.Errorf("compose.yml is missing %q", required)
		}
	}
	for _, prohibited := range []string{
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
		"GF_SECURITY_ADMIN_PASSWORD",
		"redis:",
		"postgres:",
		"0.0.0.0:8080:8080",
		"0.0.0.0:9090:9090",
		"0.0.0.0:3000:3000",
	} {
		if strings.Contains(compose, prohibited) {
			t.Errorf("compose.yml contains prohibited value %q", prohibited)
		}
	}
	if strings.Contains(compose, "127.0.0.1:9090:9090") {
		t.Error("RouteForge metrics port must remain private to the Compose network")
	}
}

func TestComposeServiceDiscoveryConfiguration(t *testing.T) {
	prometheus := readDeploymentFile(t, "../../deploy/observability/prometheus/prometheus.compose.yml")
	if !strings.Contains(prometheus, "routeforge:9090") || strings.Contains(prometheus, "127.0.0.1:9090") {
		t.Error("Compose Prometheus must scrape routeforge:9090")
	}

	compose := readDeploymentFile(t, "../../compose.yml")
	if !strings.Contains(compose, "GRAFANA_PROMETHEUS_URL: http://prometheus:9090") {
		t.Error("Compose Grafana must query Prometheus through service discovery")
	}
	datasource := readDeploymentFile(t, "../../deploy/observability/grafana/provisioning/datasources/prometheus.yml")
	if !strings.Contains(datasource, "url: ${GRAFANA_PROMETHEUS_URL}") {
		t.Error("Grafana datasource must use its deployment-provided Prometheus URL")
	}
	dashboards := readDeploymentFile(t, "../../deploy/observability/grafana/provisioning/dashboards/dashboards.yml")
	if !strings.Contains(dashboards, "path: ${GRAFANA_DASHBOARDS_PATH}") {
		t.Error("Grafana dashboard provisioning must use its deployment-provided path")
	}
}

func TestRouteForgeImageRunsNonRoot(t *testing.T) {
	dockerfile := readDeploymentFile(t, "../../Dockerfile")
	for _, required := range []string{
		"FROM golang:1.22.12-alpine3.21 AS builder",
		"FROM alpine:3.21.6",
		"CGO_ENABLED=0 GOOS=linux go build",
		"USER 65532:65532",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("Dockerfile is missing %q", required)
		}
	}
	if strings.Contains(dockerfile, "OPENAI_API_KEY") || strings.Contains(dockerfile, "ANTHROPIC_API_KEY") {
		t.Error("Dockerfile must not contain provider credential configuration")
	}
}

func readDeploymentFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
