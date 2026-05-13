package relay

import (
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestRunRetentionPrunesByRecordCount(t *testing.T) {
	for _, tt := range retentionStoreCases(t) {
		t.Run(tt.name, func(t *testing.T) {
			store := tt.store
			now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
			saveTestRun(t, store, "oldest", now.Add(-3*time.Hour), RunSucceeded, "")
			saveTestRun(t, store, "middle", now.Add(-2*time.Hour), RunSucceeded, "")
			saveTestRun(t, store, "newest", now.Add(-time.Hour), RunSucceeded, "")

			result, err := store.PruneRuns(RunRetentionConfig{MaxRecords: 2}, now)
			if err != nil {
				t.Fatal(err)
			}
			if result.Deleted != 1 {
				t.Fatalf("expected 1 deleted run, got %#v", result)
			}
			assertRunIDs(t, store, []string{"newest", "middle"})
		})
	}
}

func TestRunRetentionPrunesByAge(t *testing.T) {
	for _, tt := range retentionStoreCases(t) {
		t.Run(tt.name, func(t *testing.T) {
			store := tt.store
			now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
			saveTestRun(t, store, "expired", now.AddDate(0, 0, -8), RunSucceeded, "")
			saveTestRun(t, store, "fresh", now.AddDate(0, 0, -2), RunSucceeded, "")

			result, err := store.PruneRuns(RunRetentionConfig{MaxAgeDays: 7}, now)
			if err != nil {
				t.Fatal(err)
			}
			if result.Deleted != 1 {
				t.Fatalf("expected 1 deleted run, got %#v", result)
			}
			assertRunIDs(t, store, []string{"fresh"})
		})
	}
}

func TestRunRetentionPreservesRunningRuns(t *testing.T) {
	for _, tt := range retentionStoreCases(t) {
		t.Run(tt.name, func(t *testing.T) {
			store := tt.store
			now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
			saveTestRun(t, store, "running-old", now.AddDate(0, 0, -8), RunRunning, "")
			saveTestRun(t, store, "fresh", now.Add(-time.Hour), RunSucceeded, "")

			result, err := store.PruneRuns(RunRetentionConfig{MaxRecords: 1, MaxAgeDays: 7}, now)
			if err != nil {
				t.Fatal(err)
			}
			if result.Deleted != 0 {
				t.Fatalf("expected running run to be preserved, got %#v", result)
			}
			assertRunIDs(t, store, []string{"fresh", "running-old"})
		})
	}
}

func TestBoltStoreUsesIndexesForRecentRunsAndRunningChecks(t *testing.T) {
	store, err := OpenBoltStore(filepath.Join(t.TempDir(), "clock-relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	})

	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	saveTestRun(t, store, "oldest", now.Add(-3*time.Hour), RunSucceeded, "")
	saveTestRun(t, store, "running", now.Add(-2*time.Hour), RunRunning, "")
	saveTestRun(t, store, "newest", now.Add(-time.Hour), RunSucceeded, "")

	running, err := store.HasRunningRun("retention")
	if err != nil {
		t.Fatal(err)
	}
	if !running {
		t.Fatal("expected running run lookup to use the running-run index")
	}

	assertRunIDs(t, store, []string{"newest", "running", "oldest"})
}

func TestBoltStoreRebuildsRunIndexesForExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clock-relay.db")
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	if err := writeLegacyRunDB(path, []Run{
		testRun("oldest", now.Add(-3*time.Hour), RunSucceeded),
		testRun("running", now.Add(-2*time.Hour), RunRunning),
		testRun("newest", now.Add(-time.Hour), RunSucceeded),
	}); err != nil {
		t.Fatal(err)
	}

	store, err := OpenBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	})

	running, err := store.HasRunningRun("retention")
	if err != nil {
		t.Fatal(err)
	}
	if !running {
		t.Fatal("expected running-run index to be rebuilt")
	}
	assertRunIDs(t, store, []string{"newest", "running", "oldest"})
}

func TestEnginePrunesRunHistoryOnStartup(t *testing.T) {
	store := NewMemoryStore()
	now := time.Now().UTC()
	saveTestRun(t, store, "oldest", now.Add(-3*time.Hour), RunSucceeded, "")
	saveTestRun(t, store, "middle", now.Add(-2*time.Hour), RunSucceeded, "")
	saveTestRun(t, store, "newest", now.Add(-time.Hour), RunSucceeded, "")

	_, err := NewEngine(Config{
		Store:        StoreConfig{Type: "memory"},
		RunRetention: RunRetentionConfig{MaxRecords: 2},
	}, store, nilLogger())
	if err != nil {
		t.Fatal(err)
	}

	assertRunIDs(t, store, []string{"newest", "middle"})
}

func TestEnginePrunesRunHistoryOnCadenceAfterFinalRuns(t *testing.T) {
	store := NewMemoryStore()
	now := time.Now().UTC()
	saveTestRun(t, store, "oldest", now.Add(-3*time.Hour), RunSucceeded, "")
	saveTestRun(t, store, "middle", now.Add(-2*time.Hour), RunSucceeded, "")
	saveTestRun(t, store, "newest", now.Add(-time.Hour), RunSucceeded, "")

	engine, err := NewEngine(Config{
		Store:        StoreConfig{Type: "memory"},
		RunRetention: RunRetentionConfig{MaxRecords: 10},
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

type retentionStoreCase struct {
	name  string
	store Store
}

func retentionStoreCases(t *testing.T) []retentionStoreCase {
	t.Helper()
	boltStore, err := OpenBoltStore(filepath.Join(t.TempDir(), "clock-relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := boltStore.Close(); err != nil {
			t.Fatal(err)
		}
	})
	return []retentionStoreCase{
		{name: "memory", store: NewMemoryStore()},
		{name: "bbolt", store: boltStore},
	}
}

func saveTestRun(t *testing.T, store Store, id string, startedAt time.Time, status RunStatus, raw string) {
	t.Helper()
	run := testRun(id, startedAt, status)
	if raw != "" {
		run.StructuredOutput = map[string]any{"raw": raw}
	}
	if err := store.SaveRun(run); err != nil {
		t.Fatal(err)
	}
}

func testRun(id string, startedAt time.Time, status RunStatus) Run {
	run := Run{
		ID:           id,
		ScheduleName: "retention",
		TargetType:   "http",
		TriggeredBy:  "manual",
		Status:       status,
		ScheduledAt:  startedAt,
		StartedAt:    startedAt,
	}
	if status != RunRunning {
		finishedAt := startedAt.Add(time.Second)
		run.FinishedAt = &finishedAt
	}
	return run
}

func writeLegacyRunDB(path string, runs []Run) error {
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(runsBucket)
		if err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(schedulesBucket); err != nil {
			return err
		}
		for _, run := range runs {
			raw, err := json.Marshal(run)
			if err != nil {
				return err
			}
			if err := bucket.Put([]byte(run.ID), raw); err != nil {
				return err
			}
		}
		return nil
	})
}

func assertRunIDs(t *testing.T, store Store, want []string) {
	t.Helper()
	runs, err := store.ListRuns(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != len(want) {
		t.Fatalf("expected %d runs, got %#v", len(want), runs)
	}
	for i, id := range want {
		if runs[i].ID != id {
			t.Fatalf("expected run %d to be %q, got %#v", i, id, runs)
		}
	}
}

func nilLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
