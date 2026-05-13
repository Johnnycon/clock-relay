package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"slices"
	"time"

	"github.com/johnnycon/clock-relay/relay"
	bolt "go.etcd.io/bbolt"
)

var (
	runsBucket                  = []byte("runs")
	runStatsBucket              = []byte("run_stats")
	runsByStartedAtBucket       = []byte("runs_by_started_at")
	runningRunsByScheduleBucket = []byte("running_runs_by_schedule")
	completedRunCountKey        = []byte("completed_count")
)

const runStartedAtIndexTimeLayout = "20060102T150405.000000000Z"

func main() {
	dbPath := flag.String("db", "/tmp/clock-relay-retention-loadtest.db", "path to load-test bbolt database")
	mode := flag.String("mode", "indexed", "retention mode: indexed or scan-sort")
	scenario := flag.String("scenario", "steady", "scenario: cleanup or steady")
	runs := flag.Int("runs", 100_000, "existing run count")
	retain := flag.Int("retain", 10_000, "completed runs to retain for cleanup scenario")
	steadyInsert := flag.Int("steady-insert", 100, "new runs inserted before steady-state prune")
	payloadBytes := flag.Int("payload-bytes", 128, "bytes of synthetic output per run")
	maxAgeDays := flag.Int("max-age-days", 0, "optional max age retention limit")
	reset := flag.Bool("reset", true, "remove the database before populating")
	flag.Parse()

	if *reset {
		if err := os.Remove(*dbPath); err != nil && !os.IsNotExist(err) {
			fatal(err)
		}
	}

	existingRuns := *runs
	retentionRecords := *retain
	if *scenario == "steady" {
		retentionRecords = *runs
	}

	populateStart := time.Now()
	base := time.Now().UTC().Add(-time.Duration(existingRuns+*steadyInsert) * time.Second)
	if err := insertRuns(*dbPath, 0, existingRuns, base, *payloadBytes); err != nil {
		fatal(err)
	}
	populateElapsed := time.Since(populateStart)

	insertElapsed := time.Duration(0)
	if *scenario == "steady" {
		insertStart := time.Now()
		if err := insertRuns(*dbPath, existingRuns, *steadyInsert, base, *payloadBytes); err != nil {
			fatal(err)
		}
		insertElapsed = time.Since(insertStart)
	}

	beforeSize := fileSize(*dbPath)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	pruneStart := time.Now()
	var result relay.RunRetentionResult
	var err error
	switch *mode {
	case "indexed":
		store, openErr := relay.OpenBoltStore(*dbPath)
		err = openErr
		if err != nil {
			fatal(err)
		}
		result, err = store.PruneRuns(relay.RunRetentionConfig{MaxRecords: retentionRecords, MaxAgeDays: *maxAgeDays}, time.Now().UTC())
		if closeErr := store.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	case "scan-sort":
		result, err = scanSortPrune(*dbPath, relay.RunRetentionConfig{MaxRecords: retentionRecords, MaxAgeDays: *maxAgeDays}, time.Now().UTC())
	default:
		err = fmt.Errorf("unsupported mode %q", *mode)
	}
	if err != nil {
		fatal(err)
	}
	pruneElapsed := time.Since(pruneStart)

	runtime.ReadMemStats(&after)
	afterSize := fileSize(*dbPath)
	fmt.Printf("mode=%s scenario=%s runs=%d steady_insert=%d retain=%d deleted=%d kept=%d populate_time=%s insert_time=%s prune_time=%s heap_alloc_before=%d heap_alloc_after=%d total_alloc_delta=%d db_size_before=%d db_size_after=%d\n",
		*mode,
		*scenario,
		existingRuns,
		*steadyInsert,
		retentionRecords,
		result.Deleted,
		result.Kept,
		populateElapsed,
		insertElapsed,
		pruneElapsed,
		before.HeapAlloc,
		after.HeapAlloc,
		after.TotalAlloc-before.TotalAlloc,
		beforeSize,
		afterSize,
	)
}

func insertRuns(path string, start int, count int, base time.Time, payloadBytes int) error {
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		return err
	}
	defer db.Close()

	payload := make([]byte, payloadBytes)
	for i := range payload {
		payload[i] = 'x'
	}
	const batchSize = 10_000
	inserted := 0
	for inserted < count {
		batchCount := min(batchSize, count-inserted)
		if err := db.Update(func(tx *bolt.Tx) error {
			if err := ensureLoadTestBuckets(tx); err != nil {
				return err
			}
			for i := 0; i < batchCount; i++ {
				n := start + inserted + i
				startedAt := base.Add(time.Duration(n) * time.Second)
				finishedAt := startedAt.Add(time.Second)
				run := relay.Run{
					ID:           fmt.Sprintf("load-%012d", n),
					ScheduleName: "load-test",
					TargetType:   "http",
					TriggeredBy:  "scheduler",
					Status:       relay.RunSucceeded,
					ScheduledAt:  startedAt,
					StartedAt:    startedAt,
					FinishedAt:   &finishedAt,
					StructuredOutput: map[string]any{
						"raw": string(payload),
					},
				}
				raw, err := json.Marshal(run)
				if err != nil {
					return err
				}
				if err := tx.Bucket(runsBucket).Put([]byte(run.ID), raw); err != nil {
					return err
				}
				if err := tx.Bucket(runsByStartedAtBucket).Put(runStartedAtIndexKey(run), []byte(run.ID)); err != nil {
					return err
				}
			}
			count, err := completedRunCount(tx)
			if err != nil {
				return err
			}
			return setCompletedRunCount(tx, count+batchCount)
		}); err != nil {
			return err
		}
		inserted += batchCount
		if inserted%100_000 == 0 {
			fmt.Fprintf(os.Stderr, "inserted=%d\n", inserted)
		}
	}
	return nil
}

func ensureLoadTestBuckets(tx *bolt.Tx) error {
	for _, name := range [][]byte{runsBucket, runStatsBucket, runsByStartedAtBucket, runningRunsByScheduleBucket} {
		if _, err := tx.CreateBucketIfNotExists(name); err != nil {
			return err
		}
	}
	return nil
}

func runStartedAtIndexKey(run relay.Run) []byte {
	return []byte(run.StartedAt.UTC().Format(runStartedAtIndexTimeLayout) + "|" + run.ID)
}

func completedRunCount(tx *bolt.Tx) (int, error) {
	raw := tx.Bucket(runStatsBucket).Get(completedRunCountKey)
	if raw == nil {
		return 0, nil
	}
	if len(raw) != 8 {
		return 0, fmt.Errorf("invalid completed run count")
	}
	return int(binary.BigEndian.Uint64(raw)), nil
}

func setCompletedRunCount(tx *bolt.Tx, count int) error {
	raw := make([]byte, 8)
	binary.BigEndian.PutUint64(raw, uint64(count))
	return tx.Bucket(runStatsBucket).Put(completedRunCountKey, raw)
}

type storedRun struct {
	id  string
	run relay.Run
}

func scanSortPrune(path string, retention relay.RunRetentionConfig, now time.Time) (relay.RunRetentionResult, error) {
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		return relay.RunRetentionResult{}, err
	}
	defer db.Close()

	var result relay.RunRetentionResult
	err = db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(runsBucket)
		runs := []storedRun{}
		if err := bucket.ForEach(func(key, value []byte) error {
			var run relay.Run
			if err := json.Unmarshal(value, &run); err != nil {
				return err
			}
			runs = append(runs, storedRun{id: string(key), run: run})
			return nil
		}); err != nil {
			return err
		}

		slices.SortFunc(runs, func(a, b storedRun) int {
			return b.run.StartedAt.Compare(a.run.StartedAt)
		})

		victims := map[string]struct{}{}
		if retention.MaxAgeDays > 0 {
			cutoff := now.AddDate(0, 0, -retention.MaxAgeDays)
			for _, run := range runs {
				if run.run.Status != relay.RunRunning && run.run.StartedAt.Before(cutoff) {
					victims[run.id] = struct{}{}
				}
			}
		}
		if retention.MaxRecords > 0 {
			kept := 0
			for _, run := range runs {
				if _, deleted := victims[run.id]; !deleted && run.run.Status != relay.RunRunning {
					kept++
				}
			}
			for i := len(runs) - 1; kept > retention.MaxRecords && i >= 0; i-- {
				run := runs[i]
				if run.run.Status == relay.RunRunning {
					continue
				}
				if _, deleted := victims[run.id]; deleted {
					continue
				}
				victims[run.id] = struct{}{}
				kept--
			}
		}
		for id := range victims {
			if err := bucket.Delete([]byte(id)); err != nil {
				return err
			}
		}
		result.Deleted = len(victims)
		result.Kept = completedCount(runs, victims)
		return nil
	})
	return result, err
}

func completedCount(runs []storedRun, victims map[string]struct{}) int {
	count := 0
	for _, run := range runs {
		if _, deleted := victims[run.id]; deleted {
			continue
		}
		if run.run.Status != relay.RunRunning {
			count++
		}
	}
	return count
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
