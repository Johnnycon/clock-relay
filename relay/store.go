package relay

import (
	"encoding/json"
	"errors"
	"slices"
	"sync"

	bolt "go.etcd.io/bbolt"
)

var (
	runsBucket      = []byte("runs")
	schedulesBucket = []byte("schedules")
)

type Store interface {
	SaveSchedule(schedule ScheduleConfig) error
	DeleteSchedule(name string) error
	ListSchedules() ([]ScheduleConfig, error)
	SaveRun(run Run) error
	UpdateRun(run Run) error
	ListRuns(limit int) ([]Run, error)
	ClearRuns() error
	HasRunningRun(scheduleName string) (bool, error)
	Close() error
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
		_, err := tx.CreateBucketIfNotExists(schedulesBucket)
		return err
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
		return tx.Bucket(runsBucket).ForEach(func(_, value []byte) error {
			var run Run
			if err := json.Unmarshal(value, &run); err != nil {
				return err
			}
			runs = append(runs, run)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sortRuns(runs)
	return trimRuns(runs, limit), nil
}

func (s *BoltStore) ClearRuns() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.DeleteBucket(runsBucket); err != nil {
			return err
		}
		_, err := tx.CreateBucket(runsBucket)
		return err
	})
}

var errFound = errors.New("found")

func (s *BoltStore) HasRunningRun(scheduleName string) (bool, error) {
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(runsBucket).ForEach(func(_, value []byte) error {
			var run Run
			if err := json.Unmarshal(value, &run); err != nil {
				return err
			}
			if run.ScheduleName == scheduleName && run.Status == RunRunning {
				return errFound
			}
			return nil
		})
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
		return tx.Bucket(runsBucket).Put([]byte(run.ID), raw)
	})
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
