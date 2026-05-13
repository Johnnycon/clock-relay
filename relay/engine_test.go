package relay

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testHTTPTarget(t *testing.T) TargetConfig {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return TargetConfig{Type: "http", URL: srv.URL, Method: "POST"}
}

func TestManualTriggerRecordsRun(t *testing.T) {
	target := testHTTPTarget(t)
	cfg := Config{
		Server: ServerConfig{Addr: ":0"},
		Store:  StoreConfig{Type: "memory"},
		Schedules: []ScheduleConfig{
			{
				Name:              "hello",
				Cron:              "* * * * *",
				Timezone:          "UTC",
				Timeout:           Duration{Duration: 2 * time.Second},
				ConcurrencyPolicy: "allow",
				Target:            target,
			},
		},
	}
	store := NewMemoryStore()
	engine, err := NewEngine(cfg, store, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	run, err := engine.TriggerManual(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != RunRunning {
		t.Fatalf("expected running status, got %s", run.Status)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := store.ListRuns(1)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) == 1 && runs[0].Status == RunSucceeded {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run did not succeed before deadline")
}

func TestSaveAndDeleteSchedule(t *testing.T) {
	target := testHTTPTarget(t)
	cfg := Config{Store: StoreConfig{Type: "memory"}}
	store := NewMemoryStore()
	engine, err := NewEngine(cfg, store, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	schedule := ScheduleConfig{
		Name:              "cleanup",
		Cron:              "*/10 * * * *",
		Timezone:          "UTC",
		Timeout:           Duration{Duration: 2 * time.Second},
		ConcurrencyPolicy: "forbid",
		Target:            target,
	}
	if err := engine.SaveSchedule("", schedule); err != nil {
		t.Fatal(err)
	}
	if got := engine.Schedules(); len(got) != 1 || got[0].Name != "cleanup" {
		t.Fatalf("expected saved schedule, got %#v", got)
	}

	schedule.Name = "cleanup-renamed"
	if err := engine.SaveSchedule("cleanup", schedule); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.TriggerManual(context.Background(), "cleanup"); err == nil {
		t.Fatal("expected old schedule name to be removed")
	}
	if _, err := engine.TriggerManual(context.Background(), "cleanup-renamed"); err != nil {
		t.Fatal(err)
	}

	if err := engine.DeleteSchedule("cleanup-renamed"); err != nil {
		t.Fatal(err)
	}
	if got := engine.Schedules(); len(got) != 0 {
		t.Fatalf("expected no schedules, got %#v", got)
	}
}

func TestSaveScheduleRejectsDuplicateCreate(t *testing.T) {
	target := testHTTPTarget(t)
	cfg := Config{Store: StoreConfig{Type: "memory"}}
	store := NewMemoryStore()
	engine, err := NewEngine(cfg, store, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	schedule := ScheduleConfig{
		Name:              "duplicate",
		Cron:              "*/10 * * * *",
		Timezone:          "UTC",
		Timeout:           Duration{Duration: 2 * time.Second},
		ConcurrencyPolicy: "forbid",
		Target:            target,
	}
	if err := engine.SaveSchedule("", schedule); err != nil {
		t.Fatal(err)
	}
	if err := engine.SaveSchedule("", schedule); err == nil {
		t.Fatal("expected duplicate create to fail")
	}
}

func TestRateScheduleNextUsesPersistedAnchor(t *testing.T) {
	start := time.Date(2026, 5, 8, 10, 30, 0, 0, time.UTC)
	schedule := rateSchedule{start: start, interval: 15 * time.Minute}

	if got := schedule.Next(start.Add(-time.Second)); !got.Equal(start) {
		t.Fatalf("expected first run at anchor, got %s", got)
	}
	if got := schedule.Next(start); !got.Equal(start.Add(15 * time.Minute)) {
		t.Fatalf("expected next run after anchor, got %s", got)
	}
	if got := schedule.Next(start.Add(31 * time.Minute)); !got.Equal(start.Add(45 * time.Minute)) {
		t.Fatalf("expected next interval after elapsed time, got %s", got)
	}
}

func TestOneTimeScheduleMarksCompletedAfterScheduledTrigger(t *testing.T) {
	target := testHTTPTarget(t)
	store := NewMemoryStore()
	runAt := localDateTimeString(time.Now().UTC().Add(time.Second))
	cfg := Config{
		Store: StoreConfig{Type: "memory"},
		Schedules: []ScheduleConfig{
			{
				Name:              "once",
				ScheduleType:      "once",
				RunAt:             runAt,
				Timezone:          "UTC",
				Timeout:           Duration{Duration: 2 * time.Second},
				ConcurrencyPolicy: "forbid",
				Target:            target,
			},
		},
	}
	engine, err := NewEngine(cfg, store, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(); err != nil {
		t.Fatal(err)
	}
	defer engine.Stop()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		schedules := engine.Schedules()
		if len(schedules) == 1 && schedules[0].CompletedAt != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("one-time schedule was not marked completed")
}

func TestSaveSchedulePreservesCompletedOnceWhenRunAtUnchanged(t *testing.T) {
	target := testHTTPTarget(t)
	store := NewMemoryStore()
	engine, err := NewEngine(Config{Store: StoreConfig{Type: "memory"}}, store, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	completedAt := time.Now().UTC()
	schedule := ScheduleConfig{
		Name:              "once",
		ScheduleType:      "once",
		RunAt:             "2026-05-10T09:00",
		Timezone:          "UTC",
		CompletedAt:       &completedAt,
		Timeout:           Duration{Duration: 2 * time.Second},
		ConcurrencyPolicy: "forbid",
		Target:            target,
	}
	if err := engine.SaveSchedule("", schedule); err != nil {
		t.Fatal(err)
	}
	schedule.Description = "updated"
	schedule.CompletedAt = nil
	if err := engine.SaveSchedule("once", schedule); err != nil {
		t.Fatal(err)
	}
	schedules := engine.Schedules()
	if len(schedules) != 1 || schedules[0].CompletedAt == nil {
		t.Fatalf("expected completed_at to be preserved, got %#v", schedules)
	}
}

func TestSaveScheduleClearsCompletedOnceWhenTimezoneChanges(t *testing.T) {
	target := testHTTPTarget(t)
	store := NewMemoryStore()
	engine, err := NewEngine(Config{Store: StoreConfig{Type: "memory"}}, store, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	completedAt := time.Now().UTC()
	schedule := ScheduleConfig{
		Name:              "once",
		ScheduleType:      "once",
		RunAt:             "2026-05-10T09:00",
		Timezone:          "UTC",
		CompletedAt:       &completedAt,
		Timeout:           Duration{Duration: 2 * time.Second},
		ConcurrencyPolicy: "forbid",
		Target:            target,
	}
	if err := engine.SaveSchedule("", schedule); err != nil {
		t.Fatal(err)
	}
	schedule.Timezone = "America/Chicago"
	schedule.CompletedAt = nil
	if err := engine.SaveSchedule("once", schedule); err != nil {
		t.Fatal(err)
	}
	schedules := engine.Schedules()
	if len(schedules) != 1 || schedules[0].CompletedAt != nil {
		t.Fatalf("expected completed_at to be cleared after timezone change, got %#v", schedules)
	}
}

func TestClearRunsKeepsSchedules(t *testing.T) {
	target := testHTTPTarget(t)
	cfg := Config{Store: StoreConfig{Type: "memory"}}
	store := NewMemoryStore()
	engine, err := NewEngine(cfg, store, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	schedule := ScheduleConfig{
		Name:              "clear-runs",
		Cron:              "*/10 * * * *",
		Timezone:          "UTC",
		Timeout:           Duration{Duration: 2 * time.Second},
		ConcurrencyPolicy: "allow",
		Target:            target,
	}
	if err := engine.SaveSchedule("", schedule); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.TriggerManual(context.Background(), "clear-runs"); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := engine.Runs(10)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) > 0 && runs[0].Status == RunSucceeded {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := engine.ClearRuns(); err != nil {
		t.Fatal(err)
	}
	runs, err := engine.Runs(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected no runs after clear, got %#v", runs)
	}
	if got := engine.Schedules(); len(got) != 1 || got[0].Name != "clear-runs" {
		t.Fatalf("expected schedule to remain after clear, got %#v", got)
	}
}
