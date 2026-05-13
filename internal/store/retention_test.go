package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/johnnycon/clock-relay/internal/config"
	"github.com/johnnycon/clock-relay/internal/model"
	bolt "go.etcd.io/bbolt"
)

func TestRunRetentionPrunesByRecordCount(t *testing.T) {
	for _, tt := range retentionStoreCases(t) {
		t.Run(tt.name, func(t *testing.T) {
			store := tt.store
			now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
			saveTestRun(t, store, "oldest", now.Add(-3*time.Hour), model.RunSucceeded, "")
			saveTestRun(t, store, "middle", now.Add(-2*time.Hour), model.RunSucceeded, "")
			saveTestRun(t, store, "newest", now.Add(-time.Hour), model.RunSucceeded, "")

			result, err := store.PruneRuns(config.RunRetentionConfig{MaxRecords: 2}, now)
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
			saveTestRun(t, store, "expired", now.AddDate(0, 0, -8), model.RunSucceeded, "")
			saveTestRun(t, store, "fresh", now.AddDate(0, 0, -2), model.RunSucceeded, "")

			result, err := store.PruneRuns(config.RunRetentionConfig{MaxAgeDays: 7}, now)
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
			saveTestRun(t, store, "running-old", now.AddDate(0, 0, -8), model.RunRunning, "")
			saveTestRun(t, store, "fresh", now.Add(-time.Hour), model.RunSucceeded, "")

			result, err := store.PruneRuns(config.RunRetentionConfig{MaxRecords: 1, MaxAgeDays: 7}, now)
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
	saveTestRun(t, store, "oldest", now.Add(-3*time.Hour), model.RunSucceeded, "")
	saveTestRun(t, store, "running", now.Add(-2*time.Hour), model.RunRunning, "")
	saveTestRun(t, store, "newest", now.Add(-time.Hour), model.RunSucceeded, "")

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
	if err := writeLegacyRunDB(path, []model.Run{
		testRun("oldest", now.Add(-3*time.Hour), model.RunSucceeded),
		testRun("running", now.Add(-2*time.Hour), model.RunRunning),
		testRun("newest", now.Add(-time.Hour), model.RunSucceeded),
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

func saveTestRun(t *testing.T, store Store, id string, startedAt time.Time, status model.RunStatus, raw string) {
	t.Helper()
	run := testRun(id, startedAt, status)
	if raw != "" {
		run.StructuredOutput = map[string]any{"raw": raw}
	}
	if err := store.SaveRun(run); err != nil {
		t.Fatal(err)
	}
}

func testRun(id string, startedAt time.Time, status model.RunStatus) model.Run {
	run := model.Run{
		ID:           id,
		ScheduleName: "retention",
		TargetType:   "http",
		TriggeredBy:  "manual",
		Status:       status,
		ScheduledAt:  startedAt,
		StartedAt:    startedAt,
	}
	if status != model.RunRunning {
		finishedAt := startedAt.Add(time.Second)
		run.FinishedAt = &finishedAt
	}
	return run
}

func writeLegacyRunDB(path string, runs []model.Run) error {
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
