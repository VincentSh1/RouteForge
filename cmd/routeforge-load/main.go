package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/VincentSh1/RouteForge/internal/loadtest"
)

func run(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("routeforge-load", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var config loadtest.Config
	flags.IntVar(&config.Port, "port", 18080, "literal loopback inference port")
	flags.IntVar(&config.Concurrency, "concurrency", 1, "workers, 1–64")
	flags.IntVar(&config.Requests, "requests", 1000, "measured request cap, 1–20000")
	flags.IntVar(&config.Warmup, "warmup", 128, "excluded warm-up requests, 1–2000")
	flags.DurationVar(&config.MaxDuration, "duration", 30*time.Second, "deadline for each warm-up/measured phase, at most 5m")
	flags.DurationVar(&config.RequestTimeout, "timeout", 5*time.Second, "per-request deadline, at most 30s")
	flags.BoolVar(&config.Streaming, "stream", false, "measure streaming TTFC and duration")
	flags.StringVar(&config.CacheMode, "cache-mode", "disabled", "miss, warm_hit, disabled, or streaming bypass")
	flags.BoolVar(&config.Persistence, "persistence", false, "expected PostgreSQL persistence setting")
	metricsPort := flags.Int("metrics-port", 0, "optional literal loopback metrics port for observed deltas")
	confirmed := flags.Bool("confirm-mock", false, "confirm the target is an isolated mock-only RouteForge process")
	if flags.Parse(args) != nil || flags.NArg() != 0 || !*confirmed {
		return fmt.Errorf("invalid arguments; use bounded load flags and -confirm-mock only for an isolated mock target")
	}
	report, err := loadtest.Run(ctx, config, *metricsPort)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if encoder.Encode(report) != nil {
		return fmt.Errorf("could not write load report")
	}
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
