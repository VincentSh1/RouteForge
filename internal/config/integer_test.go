package config

import "testing"

func TestPositiveIntegerValidationOrderAndMessages(t *testing.T) {
	setProviderDefaults(t)
	keys := []string{"ROUTEFORGE_CIRCUIT_FAILURE_THRESHOLD", "ROUTEFORGE_ROUTING_MIN_SAMPLES", "ROUTEFORGE_ROUTING_EXPLORATION_INTERVAL"}
	for _, key := range keys {
		t.Setenv(key, "invalid")
	}
	for _, key := range keys {
		_, err := Load()
		if err == nil || err.Error() != key+" must be a positive integer" {
			t.Fatal("validation order or message changed")
		}
		t.Setenv(key, "7")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CircuitFailureThreshold != 7 || cfg.RoutingMinSamples != 7 || cfg.RoutingExplorationInterval != 7 {
		t.Fatal("integer settings not assigned")
	}
}
