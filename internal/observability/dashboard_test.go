package observability

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const dashboardPath = "../../deploy/observability/grafana/dashboards/routeforge-overview.json"

const alertsPath = "../../deploy/observability/prometheus/alerts.yml"

type dashboardDefinition struct {
	Title  string `json:"title"`
	UID    string `json:"uid"`
	Panels []struct {
		Title   string `json:"title"`
		Targets []struct {
			Expression string `json:"expr"`
		} `json:"targets"`
	} `json:"panels"`
	Templating struct {
		List []struct {
			Name string `json:"name"`
		} `json:"list"`
	} `json:"templating"`
}

func TestDashboardUsesCurrentMetricCatalog(t *testing.T) {
	dashboard := loadDashboard(t)
	if dashboard.Title != "RouteForge Overview" || dashboard.UID != "routeforge-overview" {
		t.Fatalf("dashboard identity = %q/%q", dashboard.Title, dashboard.UID)
	}

	knownMetrics := map[string]bool{
		"routeforge_requests_total":                   true,
		"routeforge_request_duration_seconds_bucket":  true,
		"routeforge_routing_selections_total":         true,
		"routeforge_provider_attempts_total":          true,
		"routeforge_provider_duration_seconds_bucket": true,
		"routeforge_provider_ttfc_seconds_bucket":     true,
		"routeforge_fallbacks_total":                  true,
		"routeforge_circuit_transitions_total":        true,
		"routeforge_tokens_total":                     true,
		"routeforge_estimated_cost_micro_usd_total":   true,
	}
	metricName := regexp.MustCompile(`routeforge_[a-z_]+(?:_total|_bucket)`)
	for _, panel := range dashboard.Panels {
		for _, target := range panel.Targets {
			for _, name := range metricName.FindAllString(target.Expression, -1) {
				if !knownMetrics[name] {
					t.Errorf("panel %q references unknown metric %q", panel.Title, name)
				}
			}
		}
	}

	requiredPanels := []string{
		"Request rate",
		"Success rate",
		"Fallback rate (all traffic)",
		"p50 request duration",
		"p95 request duration",
		"p50 provider duration",
		"p95 provider duration",
		"p50 streaming TTFC",
		"p95 streaming TTFC",
		"Initial selections / sec",
		"Provider outcomes / sec",
		"Fallback paths in selected range",
		"Circuit transitions in selected range",
		"Tokens in selected range",
		"Estimated cost in selected range by provider",
	}
	for _, title := range requiredPanels {
		if !dashboardHasPanel(dashboard, title) {
			t.Errorf("dashboard is missing panel %q", title)
		}
	}
}

func TestDashboardPromQLPreservesMetricSemantics(t *testing.T) {
	dashboard := loadDashboard(t)
	counters := []string{
		"routeforge_requests_total",
		"routeforge_routing_selections_total",
		"routeforge_provider_attempts_total",
		"routeforge_fallbacks_total",
		"routeforge_circuit_transitions_total",
		"routeforge_tokens_total",
		"routeforge_estimated_cost_micro_usd_total",
	}
	for _, panel := range dashboard.Panels {
		for _, target := range panel.Targets {
			expression := target.Expression
			for _, counter := range counters {
				if strings.Contains(expression, counter) && !strings.Contains(expression, "rate(") && !strings.Contains(expression, "increase(") {
					t.Errorf("panel %q reads counter %q without rate or increase", panel.Title, counter)
				}
			}
			if strings.Contains(expression, "histogram_quantile(") && !strings.Contains(expression, "_bucket") {
				t.Errorf("panel %q calculates a quantile without histogram buckets", panel.Title)
			}
			if strings.Contains(expression, "routeforge_estimated_cost_micro_usd_total") && !strings.Contains(expression, "/ 1000000") {
				t.Errorf("panel %q does not convert micro-USD to USD", panel.Title)
			}
		}
	}
}

func TestDashboardVariablesRemainBounded(t *testing.T) {
	dashboard := loadDashboard(t)
	var names []string
	for _, variable := range dashboard.Templating.List {
		names = append(names, variable.Name)
	}
	slices.Sort(names)
	want := []string{"provider", "routing_policy", "streaming"}
	if !slices.Equal(names, want) {
		t.Fatalf("dashboard variables = %v, want %v", names, want)
	}

	data, err := os.ReadFile(dashboardPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, prohibited := range []string{"request_id", "trace_id", "user_id", "authorization", "api_key", "model=~"} {
		if strings.Contains(strings.ToLower(string(data)), prohibited) {
			t.Errorf("dashboard contains prohibited high-cardinality or sensitive field %q", prohibited)
		}
	}
}

func TestAlertRulesUseCurrentMetricCatalog(t *testing.T) {
	data, err := os.ReadFile(alertsPath)
	if err != nil {
		t.Fatal(err)
	}
	knownMetrics := map[string]bool{
		"routeforge_requests_total":                  true,
		"routeforge_request_duration_seconds_bucket": true,
		"routeforge_provider_attempts_total":         true,
		"routeforge_provider_ttfc_seconds_bucket":    true,
		"routeforge_provider_ttfc_seconds_count":     true,
		"routeforge_fallbacks_total":                 true,
		"routeforge_circuit_transitions_total":       true,
	}
	metricName := regexp.MustCompile(`routeforge_[a-z_]+(?:_total|_bucket|_count)`)
	for _, name := range metricName.FindAllString(string(data), -1) {
		if !knownMetrics[name] {
			t.Errorf("alert rules reference unknown metric %q", name)
		}
	}
	for _, alert := range []string{
		"RouteForgeHighRequestFailureRate",
		"RouteForgeHighFallbackRate",
		"RouteForgeProviderTimeouts",
		"RouteForgeProviderRateLimited",
		"RouteForgeCircuitOpening",
		"RouteForgeHighP95Latency",
		"RouteForgeHighStreamingTTFC",
	} {
		if !strings.Contains(string(data), "alert: "+alert) {
			t.Errorf("alert rules are missing %q", alert)
		}
	}
}

func dashboardHasPanel(dashboard dashboardDefinition, title string) bool {
	for _, panel := range dashboard.Panels {
		if panel.Title == title {
			return true
		}
	}
	return false
}

func loadDashboard(t *testing.T) dashboardDefinition {
	t.Helper()
	data, err := os.ReadFile(dashboardPath)
	if err != nil {
		t.Fatal(err)
	}
	var dashboard dashboardDefinition
	if err := json.Unmarshal(data, &dashboard); err != nil {
		t.Fatal(err)
	}
	return dashboard
}
