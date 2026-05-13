package relay

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	runsBucket                  = []byte("runs")
	runStatsBucket              = []byte("run_stats")
	runsByStartedAtBucket       = []byte("runs_by_started_at")
	runningRunsByScheduleBucket = []byte("running_runs_by_schedule")
	schedulesBucket             = []byte("schedules")
	completedRunCountKey        = []byte("completed_count")
)

const runStartedAtIndexTimeLayout = "20060102T150405.000000000Z"

type Store interface {
	SaveSchedule(schedule ScheduleConfig) error
	DeleteSchedule(name string) error
	ListSchedules() ([]ScheduleConfig, error)
	SaveRun(run Run) error
	UpdateRun(run Run) error
	ListRuns(limit int) ([]Run, error)
	ClearRuns() error
	PruneRuns(retention RunRetentionConfig, now time.Time) (RunRetentionResult, error)
	HasRunningRun(scheduleName string) (bool, error)
	Close() error
}

type RunRetentionResult struct {
	Deleted int
	Kept    int
}

type MemoryStore struct {
	mu        sync.RWMutex
	runs      map[string]Run
	schedules map[string]ScheduleConfig
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{runs: map[string]Run{}, schedules: map[string]ScheduleConfig{}}
}

func (s *MemoryStore) SaveSchedule(schedule ScheduleConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	NormalizeSchedule(&schedule)
	s.schedules[schedule.Name] = schedule
	return nil
}

func (s *MemoryStore) DeleteSchedule(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.schedules, name)
	return nil
}

func (s *MemoryStore) ListSchedules() ([]ScheduleConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	schedules := make([]ScheduleConfig, 0, len(s.schedules))
	for _, schedule := range s.schedules {
		schedules = append(schedules, schedule)
	}
	sortSchedules(schedules)
	return schedules, nil
}

func (s *MemoryStore) SaveRun(run Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[run.ID] = run
	return nil
}

func (s *MemoryStore) UpdateRun(run Run) error {
	return s.SaveRun(run)
}

func (s *MemoryStore) ListRuns(limit int) ([]Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	runs := make([]Run, 0, len(s.runs))
	for _, run := range s.runs {
		runs = append(runs, run)
	}
	sortRuns(runs)
	return trimRuns(runs, limit), nil
}

func (s *MemoryStore) ClearRuns() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs = map[string]Run{}
	return nil
}

func (s *MemoryStore) PruneRuns(retention RunRetentionConfig, now time.Time) (RunRetentionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored := make([]storedRun, 0, len(s.runs))
	for id, run := range s.runs {
		stored = append(stored, storedRun{id: id, run: run})
	}
	result, victims := pruneRuns(stored, retention, now)
	for id := range victims {
		delete(s.runs, id)
	}
	return result, nil
}

func (s *MemoryStore) HasRunningRun(scheduleName string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, run := range s.runs {
		if run.ScheduleName == scheduleName && run.Status == RunRunning {
			return true, nil
		}
	}
	return false, nil
}

func (s *MemoryStore) Close() error {
	return nil
}

type BoltStore struct {
	db *bolt.DB
}

func OpenBoltStore(path string) (*BoltStore, error) {
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		return nil, err
	}
	store := &BoltStore{db: db}
	if err := db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(runsBucket); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(schedulesBucket); err != nil {
			return err
		}
		needsIndexRebuild := tx.Bucket(runsByStartedAtBucket) == nil ||
			tx.Bucket(runningRunsByScheduleBucket) == nil ||
			tx.Bucket(runStatsBucket) == nil ||
			tx.Bucket(runStatsBucket).Get(completedRunCountKey) == nil
		if _, err := tx.CreateBucketIfNotExists(runsByStartedAtBucket); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(runningRunsByScheduleBucket); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(runStatsBucket); err != nil {
			return err
		}
		if needsIndexRebuild {
			return rebuildRunIndexes(tx)
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *BoltStore) SaveSchedule(schedule ScheduleConfig) error {
	NormalizeSchedule(&schedule)
	return s.db.Update(func(tx *bolt.Tx) error {
		raw, err := json.Marshal(schedule)
		if err != nil {
			return err
		}
		return tx.Bucket(schedulesBucket).Put([]byte(schedule.Name), raw)
	})
}

func (s *BoltStore) DeleteSchedule(name string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(schedulesBucket).Delete([]byte(name))
	})
}

func (s *BoltStore) ListSchedules() ([]ScheduleConfig, error) {
	var schedules []ScheduleConfig
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(schedulesBucket).ForEach(func(_, value []byte) error {
			var schedule ScheduleConfig
			if err := json.Unmarshal(value, &schedule); err != nil {
				return err
			}
			schedules = append(schedules, schedule)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sortSchedules(schedules)
	return schedules, nil
}

func (s *BoltStore) SaveRun(run Run) error {
	return s.writeRun(run)
}

func (s *BoltStore) UpdateRun(run Run) error {
	return s.writeRun(run)
}

func (s *BoltStore) ListRuns(limit int) ([]Run, error) {
	runs := []Run{}
	err := s.db.View(func(tx *bolt.Tx) error {
		runsByStartedAt := tx.Bucket(runsByStartedAtBucket)
		runsBucket := tx.Bucket(runsBucket)
		cursor := runsByStartedAt.Cursor()
		for key, value := cursor.Last(); key != nil; key, value = cursor.Prev() {
			var run Run
			raw := runsBucket.Get(value)
			if raw == nil {
				continue
			}
			if err := json.Unmarshal(raw, &run); err != nil {
				return err
			}
			runs = append(runs, run)
			if limit > 0 && len(runs) >= limit {
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return runs, nil
}

func (s *BoltStore) ClearRuns() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		for _, bucketName := range [][]byte{runsBucket, runsByStartedAtBucket, runningRunsByScheduleBucket} {
			if err := resetBucket(tx, bucketName); err != nil {
				return err
			}
		}
		if err := setCompletedRunCount(tx, 0); err != nil {
			return err
		}
		return nil
	})
}

func (s *BoltStore) PruneRuns(retention RunRetentionConfig, now time.Time) (RunRetentionResult, error) {
	var result RunRetentionResult
	err := s.db.Update(func(tx *bolt.Tx) error {
		deleted := 0
		if retention.MaxAgeDays > 0 {
			cutoff := now.AddDate(0, 0, -retention.MaxAgeDays)
			n, err := pruneRunsOlderThan(tx, cutoff)
			if err != nil {
				return err
			}
			deleted += n
		}
		if retention.MaxRecords > 0 {
			n, err := pruneRunsOverCount(tx, retention.MaxRecords)
			if err != nil {
				return err
			}
			deleted += n
		}
		count, err := completedRunCount(tx)
		if err != nil {
			return err
		}
		result = RunRetentionResult{Deleted: deleted, Kept: count}
		return nil
	})
	return result, err
}

func pruneRunsOlderThan(tx *bolt.Tx, cutoff time.Time) (int, error) {
	type victim struct {
		key []byte
		id  []byte
	}
	victims := []victim{}
	cursor := tx.Bucket(runsByStartedAtBucket).Cursor()
	cutoff = cutoff.UTC()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		startedAt, err := startedAtFromRunIndexKey(key)
		if err != nil {
			return 0, err
		}
		if !startedAt.Before(cutoff) {
			break
		}
		run, err := runByID(tx, string(value))
		if err != nil {
			return 0, err
		}
		if run.Status == RunRunning {
			continue
		}
		victims = append(victims, victim{key: slices.Clone(key), id: slices.Clone(value)})
	}
	for _, victim := range victims {
		if err := deleteRunByIndexedKey(tx, victim.key, victim.id); err != nil {
			return 0, err
		}
	}
	return len(victims), nil
}

func pruneRunsOverCount(tx *bolt.Tx, maxRecords int) (int, error) {
	count, err := completedRunCount(tx)
	if err != nil {
		return 0, err
	}
	excess := count - maxRecords
	if excess <= 0 {
		return 0, nil
	}
	type victim struct {
		key []byte
		id  []byte
	}
	victims := make([]victim, 0, excess)
	cursor := tx.Bucket(runsByStartedAtBucket).Cursor()
	for key, value := cursor.First(); key != nil && len(victims) < excess; key, value = cursor.Next() {
		run, err := runByID(tx, string(value))
		if err != nil {
			return 0, err
		}
		if run.Status == RunRunning {
			continue
		}
		victims = append(victims, victim{key: slices.Clone(key), id: slices.Clone(value)})
	}
	for _, victim := range victims {
		if err := deleteRunByIndexedKey(tx, victim.key, victim.id); err != nil {
			return 0, err
		}
	}
	return len(victims), nil
}

var errFound = errors.New("found")

func (s *BoltStore) HasRunningRun(scheduleName string) (bool, error) {
	err := s.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(runningRunsByScheduleBucket).Cursor()
		prefix := runningRunSchedulePrefix(scheduleName)
		key, _ := cursor.Seek(prefix)
		if key != nil && bytes.HasPrefix(key, prefix) {
			return errFound
		}
		return nil
	})
	if errors.Is(err, errFound) {
		return true, nil
	}
	return false, err
}

func (s *BoltStore) Close() error {
	return s.db.Close()
}

func (s *BoltStore) writeRun(run Run) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		raw, err := json.Marshal(run)
		if err != nil {
			return err
		}
		bucket := tx.Bucket(runsBucket)
		var oldRun Run
		hadOldRun := false
		if oldRaw := bucket.Get([]byte(run.ID)); oldRaw != nil {
			if err := json.Unmarshal(oldRaw, &oldRun); err != nil {
				return err
			}
			hadOldRun = true
			if err := removeRunIndexes(tx, oldRun); err != nil {
				return err
			}
		}
		if err := bucket.Put([]byte(run.ID), raw); err != nil {
			return err
		}
		if err := addRunIndexes(tx, run); err != nil {
			return err
		}
		if isCompletedRun(run) && (!hadOldRun || !isCompletedRun(oldRun)) {
			return incrementCompletedRunCount(tx, 1)
		}
		if !isCompletedRun(run) && hadOldRun && isCompletedRun(oldRun) {
			return incrementCompletedRunCount(tx, -1)
		}
		return nil
	})
}

func rebuildRunIndexes(tx *bolt.Tx) error {
	for _, bucketName := range [][]byte{runsByStartedAtBucket, runningRunsByScheduleBucket} {
		if err := resetBucket(tx, bucketName); err != nil {
			return err
		}
	}
	if err := setCompletedRunCount(tx, 0); err != nil {
		return err
	}
	count := 0
	if err := tx.Bucket(runsBucket).ForEach(func(_, value []byte) error {
		var run Run
		if err := json.Unmarshal(value, &run); err != nil {
			return err
		}
		if err := addRunIndexes(tx, run); err != nil {
			return err
		}
		if isCompletedRun(run) {
			count++
		}
		return nil
	}); err != nil {
		return err
	}
	return setCompletedRunCount(tx, count)
}

func resetBucket(tx *bolt.Tx, name []byte) error {
	if tx.Bucket(name) != nil {
		if err := tx.DeleteBucket(name); err != nil {
			return err
		}
	}
	_, err := tx.CreateBucket(name)
	return err
}

func addRunIndexes(tx *bolt.Tx, run Run) error {
	if err := tx.Bucket(runsByStartedAtBucket).Put(runStartedAtIndexKey(run), []byte(run.ID)); err != nil {
		return err
	}
	if run.Status == RunRunning {
		return tx.Bucket(runningRunsByScheduleBucket).Put(runningRunScheduleKey(run), []byte(run.ID))
	}
	return nil
}

func removeRunIndexes(tx *bolt.Tx, run Run) error {
	if err := tx.Bucket(runsByStartedAtBucket).Delete(runStartedAtIndexKey(run)); err != nil {
		return err
	}
	if run.Status == RunRunning {
		return tx.Bucket(runningRunsByScheduleBucket).Delete(runningRunScheduleKey(run))
	}
	return nil
}

func deleteRunByIndexedKey(tx *bolt.Tx, indexKey []byte, id []byte) error {
	run, err := runByID(tx, string(id))
	if err != nil {
		return err
	}
	if err := tx.Bucket(runsByStartedAtBucket).Delete(indexKey); err != nil {
		return err
	}
	if run.Status == RunRunning {
		if err := tx.Bucket(runningRunsByScheduleBucket).Delete(runningRunScheduleKey(run)); err != nil {
			return err
		}
	}
	if err := tx.Bucket(runsBucket).Delete(id); err != nil {
		return err
	}
	if isCompletedRun(run) {
		return incrementCompletedRunCount(tx, -1)
	}
	return nil
}

func runByID(tx *bolt.Tx, id string) (Run, error) {
	raw := tx.Bucket(runsBucket).Get([]byte(id))
	if raw == nil {
		return Run{}, ConfigError("run not found: " + id)
	}
	var run Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return Run{}, err
	}
	return run, nil
}

func runStartedAtIndexKey(run Run) []byte {
	return []byte(run.StartedAt.UTC().Format(runStartedAtIndexTimeLayout) + "|" + run.ID)
}

func startedAtFromRunIndexKey(key []byte) (time.Time, error) {
	if len(key) < len(runStartedAtIndexTimeLayout) {
		return time.Time{}, ConfigError("invalid run time index key")
	}
	return time.Parse(runStartedAtIndexTimeLayout, string(key[:len(runStartedAtIndexTimeLayout)]))
}

func runningRunSchedulePrefix(scheduleName string) []byte {
	return []byte(scheduleName + "\x00")
}

func runningRunScheduleKey(run Run) []byte {
	return []byte(run.ScheduleName + "\x00" + run.ID)
}

func isCompletedRun(run Run) bool {
	return run.Status != RunRunning
}

func completedRunCount(tx *bolt.Tx) (int, error) {
	raw := tx.Bucket(runStatsBucket).Get(completedRunCountKey)
	if raw == nil {
		return 0, nil
	}
	if len(raw) != 8 {
		return 0, ConfigError("invalid completed run count")
	}
	return int(binary.BigEndian.Uint64(raw)), nil
}

func setCompletedRunCount(tx *bolt.Tx, count int) error {
	if count < 0 {
		count = 0
	}
	raw := make([]byte, 8)
	binary.BigEndian.PutUint64(raw, uint64(count))
	return tx.Bucket(runStatsBucket).Put(completedRunCountKey, raw)
}

func incrementCompletedRunCount(tx *bolt.Tx, delta int) error {
	count, err := completedRunCount(tx)
	if err != nil {
		return err
	}
	return setCompletedRunCount(tx, count+delta)
}

func sortRuns(runs []Run) {
	slices.SortFunc(runs, func(a, b Run) int {
		return b.StartedAt.Compare(a.StartedAt)
	})
}

func trimRuns(runs []Run, limit int) []Run {
	if limit <= 0 || limit > len(runs) {
		return runs
	}
	return runs[:limit]
}

type storedRun struct {
	id  string
	run Run
}

func pruneRuns(runs []storedRun, retention RunRetentionConfig, now time.Time) (RunRetentionResult, map[string]struct{}) {
	victims := map[string]struct{}{}
	sortStoredRunsNewestFirst(runs)

	if retention.MaxAgeDays > 0 {
		cutoff := now.AddDate(0, 0, -retention.MaxAgeDays)
		for _, run := range runs {
			if run.run.Status != RunRunning && run.run.StartedAt.Before(cutoff) {
				victims[run.id] = struct{}{}
			}
		}
	}

	if retention.MaxRecords > 0 {
		keptCount := 0
		for _, run := range runs {
			if _, deleted := victims[run.id]; !deleted && run.run.Status != RunRunning {
				keptCount++
			}
		}
		for i := len(runs) - 1; keptCount > retention.MaxRecords && i >= 0; i-- {
			run := runs[i]
			if run.run.Status == RunRunning {
				continue
			}
			if _, deleted := victims[run.id]; deleted {
				continue
			}
			victims[run.id] = struct{}{}
			keptCount--
		}
	}

	return RunRetentionResult{
		Deleted: len(victims),
		Kept:    retainedCompletedRunCount(runs, victims),
	}, victims
}

func sortStoredRunsNewestFirst(runs []storedRun) {
	slices.SortFunc(runs, func(a, b storedRun) int {
		return b.run.StartedAt.Compare(a.run.StartedAt)
	})
}

func retainedCompletedRunCount(runs []storedRun, victims map[string]struct{}) int {
	count := 0
	for _, run := range runs {
		if _, deleted := victims[run.id]; deleted {
			continue
		}
		if isCompletedRun(run.run) {
			count++
		}
	}
	return count
}

func sortSchedules(schedules []ScheduleConfig) {
	slices.SortFunc(schedules, func(a, b ScheduleConfig) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
}
