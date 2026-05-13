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

	"github.com/johnnycon/clock-relay/relay"
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

	cfg, err := relay.LoadConfig(configPath)
	if err != nil {
		logger.Error("load config", "error", err)
		os.Exit(1)
	}
	if addr != "" {
		cfg.Server.Addr = addr
	}

	store, err := openStore(cfg)
	if err != nil {
		logger.Error("open store", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	engine, err := relay.NewEngine(cfg, store, logger)
	if err != nil {
		logger.Error("create engine", "error", err)
		os.Exit(1)
	}

	server := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           relay.NewHTTPServer(engine, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := engine.Start(); err != nil {
		logger.Error("start scheduler", "error", err)
		os.Exit(1)
	}
	defer engine.Stop()

	go func() {
		logger.Info("clock-relay listening", "addr", cfg.Server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown server", "error", err)
	}
}

func openStore(cfg relay.Config) (relay.Store, error) {
	switch cfg.Store.Type {
	case "", "bbolt":
		if err := os.MkdirAll(filepath.Dir(cfg.Store.Path), 0o755); err != nil {
			return nil, err
		}
		return relay.OpenBoltStore(cfg.Store.Path)
	case "memory":
		return relay.NewMemoryStore(), nil
	default:
		return nil, relay.ConfigError("unsupported store type: " + cfg.Store.Type)
	}
}
