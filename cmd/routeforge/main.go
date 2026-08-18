package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/VincentSh1/RouteForge/internal/config"
	"github.com/VincentSh1/RouteForge/internal/gateway"
	"github.com/VincentSh1/RouteForge/internal/httpapi"
	"github.com/VincentSh1/RouteForge/internal/provider/mock"
)

func main() {
	if err := run(); err != nil {
		slog.Error("RouteForge stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	completionProvider := &mock.Provider{}
	service := gateway.New(completionProvider)
	handler := httpapi.NewHandler(service)
	server := httpapi.NewServer(cfg, handler.Routes())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("RouteForge listening", "address", cfg.Addr, "provider", completionProvider.Name())
		serverErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}
