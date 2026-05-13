package engine

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/johnnycon/clock-relay/internal/config"
	"github.com/johnnycon/clock-relay/internal/model"
	"github.com/johnnycon/clock-relay/internal/store"
)

func TestEnginePrunesRunHistoryOnStartup(t *testing.T) {
	store := store.NewMemoryStore()
	now := time.Now().UTC()
	saveTestRun(t, store, "oldest", now.Add(-3*time.Hour), model.RunSucceeded, "")
	saveTestRun(t, store, "middle", now.Add(-2*time.Hour), model.RunSucceeded, "")
	saveTestRun(t, store, "newest", now.Add(-time.Hour), model.RunSucceeded, "")

	_, err := NewEngine(config.Config{
		Store:        config.StoreConfig{Type: "memory"},
		RunRetention: config.RunRetentionConfig{MaxRecords: 2},
	}, store, nilLogger())
	if err != nil {
		t.Fatal(err)
	}

	assertRunIDs(t, store, []string{"newest", "middle"})
}

func TestEnginePrunesRunHistoryOnCadenceAfterFinalRuns(t *testing.T) {
	store := store.NewMemoryStore()
	now := time.Now().UTC()
	saveTestRun(t, store, "oldest", now.Add(-3*time.Hour), model.RunSucceeded, "")
	saveTestRun(t, store, "middle", now.Add(-2*time.Hour), model.RunSucceeded, "")
	saveTestRun(t, store, "newest", now.Add(-time.Hour), model.RunSucceeded, "")

	engine, err := NewEngine(config.Config{
		Store:        config.StoreConfig{Type: "memory"},
		RunRetention: config.RunRetentionConfig{MaxRecords: 10},
	}, store, nilLogger())
	if err != nil {
		t.Fatal(err)
	}
	engine.cfg.RunRetention.MaxRecords = 2
	engine.maybePruneRuns()

	assertRunIDs(t, store, []string{"newest", "middle", "oldest"})

	engine.lastRunPrune = time.Now().UTC().Add(-engine.runPruneEvery)
	engine.maybePruneRuns()

	assertRunIDs(t, store, []string{"newest", "middle"})
}

func saveTestRun(t *testing.T, store store.Store, id string, startedAt time.Time, status model.RunStatus, raw string) {
	t.Helper()
	run := model.Run{
		ID:               id,
		ScheduleName:     "retention",
		TargetType:       "http",
		TriggeredBy:      "manual",
		Status:           status,
		ScheduledAt:      startedAt,
		StartedAt:        startedAt,
		StructuredOutput: map[string]any{"raw": raw},
	}
	if status != model.RunRunning {
		finishedAt := startedAt.Add(time.Second)
		run.FinishedAt = &finishedAt
	}
	if err := store.SaveRun(run); err != nil {
		t.Fatal(err)
	}
}

func assertRunIDs(t *testing.T, store store.Store, want []string) {
	t.Helper()
	runs, err := store.ListRuns(100)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(runs))
	for i, run := range runs {
		got[i] = run.ID
	}
	if len(got) != len(want) {
		t.Fatalf("expected runs %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected runs %v, got %v", want, got)
		}
	}
}

func nilLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
