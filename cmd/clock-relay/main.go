package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/johnnycon/clock-relay/internal/config"
	"github.com/johnnycon/clock-relay/internal/engine"
	"github.com/johnnycon/clock-relay/internal/server"
	"github.com/johnnycon/clock-relay/internal/store"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	var configPath string
	var addr string
	var showVersion bool
	flag.StringVar(&configPath, "config", "clock-relay.yaml", "path to Clock Relay YAML config")
	flag.StringVar(&addr, "addr", "", "HTTP listen address, overrides config")
	flag.BoolVar(&showVersion, "version", false, "print version information and exit")
	flag.Parse()

	if showVersion {
		fmt.Printf("clock-relay %s\ncommit: %s\nbuilt: %s\n", version, commit, buildDate)
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		logger.Error("load config", "error", err)
		os.Exit(1)
	}
	if addr != "" {
		cfg.Server.Addr = addr
	}

	relayStore, err := openStore(cfg)
	if err != nil {
		logger.Error("open store", "error", err)
		os.Exit(1)
	}
	defer relayStore.Close()

	relayEngine, err := engine.NewEngine(cfg, relayStore, logger)
	if err != nil {
		logger.Error("create engine", "error", err)
		os.Exit(1)
	}

	httpServer := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           server.NewHTTPServer(relayEngine, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := relayEngine.Start(); err != nil {
		logger.Error("start scheduler", "error", err)
		os.Exit(1)
	}
	defer relayEngine.Stop()

	go func() {
		logger.Info("clock-relay listening", "addr", cfg.Server.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown server", "error", err)
	}
}

func openStore(cfg config.Config) (store.Store, error) {
	switch cfg.Store.Type {
	case "", "bbolt":
		if err := os.MkdirAll(filepath.Dir(cfg.Store.Path), 0o755); err != nil {
			return nil, err
		}
		return store.OpenBoltStore(cfg.Store.Path)
	case "memory":
		return store.NewMemoryStore(), nil
	default:
		return nil, config.ConfigError("unsupported store type: " + cfg.Store.Type)
	}
}
