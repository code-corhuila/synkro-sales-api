package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/code-corhuila/synkro-sales-api/internal/adapter/in/httpapi"
	"github.com/code-corhuila/synkro-sales-api/internal/config"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := config.Load()

	srv := &http.Server{
		Addr:              ":" + cfg.HTTPPort,
		Handler:           httpapi.NewRouter(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	logger.Info("starting synkro-sales-api", "port", cfg.HTTPPort)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
