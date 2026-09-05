package main

import (
	"testing"

	"github.com/VincentSh1/RouteForge/internal/config"
	"github.com/VincentSh1/RouteForge/internal/observability"
)

func TestDisabledPersistenceInitializesNoWorkerOrDatabase(t *testing.T) {
	recorder, shutdown, err := initializePersistence(config.Config{}, observability.NoopMetrics())
	if err != nil {
		t.Fatalf("initializePersistence() error = %v", err)
	}
	if recorder.Enabled() || shutdown != nil {
		t.Fatalf("disabled persistence = recorder enabled %v, shutdown %v", recorder.Enabled(), shutdown != nil)
	}
}
