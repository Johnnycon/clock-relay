package engine

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/johnnycon/clock-relay/internal/config"
	"github.com/johnnycon/clock-relay/internal/store"
)

func TestRunLoggingSummaryOmitsRawOutput(t *testing.T) {
	var logs bytes.Buffer
	engine := runLoggingEngine(t, &logs, "summary", "secret-response-body")

	if _, err := engine.TriggerManual(context.Background(), "logging"); err != nil {
		t.Fatal(err)
	}
	waitForRunLog(t, &logs, "run event")

	output := logs.String()
	if !strings.Contains(output, "run event") {
		t.Fatalf("expected run event log, got %s", output)
	}
	if !strings.Contains(output, "http_status_code=200") {
		t.Fatalf("expected summary HTTP status code, got %s", output)
	}
	if strings.Contains(output, "secret-response-body") {
		t.Fatalf("summary logging leaked raw output: %s", output)
	}
}

func TestRunLoggingFullIncludesStructuredOutput(t *testing.T) {
	var logs bytes.Buffer
	engine := runLoggingEngine(t, &logs, "full", "full-response-body")

	if _, err := engine.TriggerManual(context.Background(), "logging"); err != nil {
		t.Fatal(err)
	}
	waitForRunLog(t, &logs, "run event")

	output := logs.String()
	if !strings.Contains(output, "full-response-body") {
		t.Fatalf("expected full logging to include raw output, got %s", output)
	}
}

func TestRunLoggingOffOmitsRunEvent(t *testing.T) {
	var logs bytes.Buffer
	engine := runLoggingEngine(t, &logs, "off", "hidden-response-body")

	if _, err := engine.TriggerManual(context.Background(), "logging"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	output := logs.String()
	if strings.Contains(output, "run event") {
		t.Fatalf("expected run event to be omitted, got %s", output)
	}
}

func runLoggingEngine(t *testing.T, logs *bytes.Buffer, stdoutMode string, responseBody string) *Engine {
	t.Helper()
	target := loggingHTTPTarget(t, responseBody)
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	engine, err := NewEngine(config.Config{
		Store:      config.StoreConfig{Type: "memory"},
		RunLogging: config.RunLoggingConfig{Stdout: stdoutMode},
		Schedules: []config.ScheduleConfig{
			{
				Name:     "logging",
				Cron:     "* * * * *",
				Timezone: "UTC",
				Timeout:  config.Duration{Duration: 2 * time.Second},
				Target:   target,
			},
		},
	}, store.NewMemoryStore(), logger)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func loggingHTTPTarget(t *testing.T, body string) config.TargetConfig {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return config.TargetConfig{Type: "http", URL: srv.URL, Method: "POST"}
}

func waitForRunLog(t *testing.T, logs *bytes.Buffer, pattern string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logs.String(), pattern) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("log %q did not appear, got %s", pattern, logs.String())
}
