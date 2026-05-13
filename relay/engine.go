package relay

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	cron "github.com/netresearch/go-cron"
)

type Engine struct {
	cfg       Config
	store     Store
	logger    *slog.Logger
	cron      *cron.Cron
	parser    cron.Parser
	schedules map[string]ScheduleConfig
	entryIDs  map[string]cron.EntryID
	mu        sync.RWMutex
	wg        sync.WaitGroup
}

func NewEngine(cfg Config, store Store, logger *slog.Logger) (*Engine, error) {
	parser, err := cron.TryNewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor | cron.DowOrDom)
	if err != nil {
		return nil, err
	}
	c := cron.New(cron.WithParser(parser), cron.WithChain(cron.Recover(cron.DefaultLogger)))
	engine := &Engine{
		cfg:       cfg,
		store:     store,
		logger:    logger,
		cron:      c,
		parser:    parser,
		schedules: map[string]ScheduleConfig{},
		entryIDs:  map[string]cron.EntryID{},
	}

	schedules, err := store.ListSchedules()
	if err != nil {
		return nil, err
	}
	if len(schedules) == 0 && len(cfg.Schedules) > 0 {
		for _, schedule := range cfg.Schedules {
			if err := store.SaveSchedule(schedule); err != nil {
				return nil, err
			}
		}
		schedules = cfg.Schedules
	}

	for _, schedule := range schedules {
		if err := engine.registerSchedule(schedule); err != nil {
			return nil, err
		}
	}

	return engine, nil
}

func (e *Engine) registerSchedule(schedule ScheduleConfig) error {
	NormalizeSchedule(&schedule)
	if err := validateSchedule(schedule, e.cfg.Faktory); err != nil {
		return err
	}
	e.schedules[schedule.Name] = schedule
	if err := e.registerScheduleLocked(schedule); err != nil {
		delete(e.schedules, schedule.Name)
		return err
	}
	return nil
}

func validateSchedule(schedule ScheduleConfig, faktoryInstances []FaktoryInstance) error {
	NormalizeSchedule(&schedule)
	if err := (Config{Faktory: faktoryInstances, Schedules: []ScheduleConfig{schedule}}).Validate(); err != nil {
		return err
	}
	if schedule.ScheduleType == "cron" {
		parser, err := cron.TryNewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor | cron.DowOrDom)
		if err != nil {
			return err
		}
		if _, err := parser.Parse(cronSpec(schedule)); err != nil {
			return fmt.Errorf("invalid cron for schedule %s: %w", schedule.Name, err)
		}
	}
	return nil
}

func cronSpec(schedule ScheduleConfig) string {
	spec := schedule.Cron
	if schedule.Timezone != "" {
		spec = "CRON_TZ=" + schedule.Timezone + " " + spec
	}
	return spec
}

func (e *Engine) cronSchedule(schedule ScheduleConfig) (cron.Schedule, error) {
	switch schedule.ScheduleType {
	case "cron":
		return e.parser.Parse(cronSpec(schedule))
	case "rate":
		start, err := parseLocalDateTime(schedule.StartsAt, schedule.Timezone)
		if err != nil {
			return nil, err
		}
		return rateSchedule{start: start, interval: rateDuration(schedule)}, nil
	case "once":
		runAt, err := parseLocalDateTime(schedule.RunAt, schedule.Timezone)
		if err != nil {
			return nil, err
		}
		fireAt := runAt
		if !fireAt.After(time.Now()) {
			fireAt = time.Now().Add(time.Second)
		}
		return onceSchedule{runAt: fireAt}, nil
	default:
		return nil, ConfigError("unsupported schedule_type for schedule " + schedule.Name + ": " + schedule.ScheduleType)
	}
}

func rateDuration(schedule ScheduleConfig) time.Duration {
	switch schedule.RateUnit {
	case "minutes":
		return time.Duration(schedule.RateInterval) * time.Minute
	case "hours":
		return time.Duration(schedule.RateInterval) * time.Hour
	case "days":
		return time.Duration(schedule.RateInterval) * 24 * time.Hour
	default:
		return 0
	}
}

func scheduledAtForSchedule(schedule ScheduleConfig, now time.Time) time.Time {
	if schedule.ScheduleType == "once" {
		runAt, err := parseLocalDateTime(schedule.RunAt, schedule.Timezone)
		if err == nil {
			return runAt.UTC()
		}
	}
	return now.UTC().Truncate(time.Minute)
}

func (e *Engine) SaveSchedule(originalName string, schedule ScheduleConfig) error {
	NormalizeSchedule(&schedule)
	if err := validateSchedule(schedule, e.cfg.Faktory); err != nil {
		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	oldSchedule, hadOriginal := ScheduleConfig{}, false
	if originalName != "" {
		oldSchedule, hadOriginal = e.schedules[originalName]
		if !hadOriginal {
			return ConfigError("schedule not found: " + originalName)
		}
		if schedule.ScheduleType == "once" && oldSchedule.ScheduleType == "once" && schedule.RunAt == oldSchedule.RunAt && schedule.Timezone == oldSchedule.Timezone && schedule.CompletedAt == nil {
			schedule.CompletedAt = oldSchedule.CompletedAt
		}
	}

	if originalName == "" {
		if _, exists := e.schedules[schedule.Name]; exists {
			return ConfigError("schedule already exists: " + schedule.Name)
		}
	}
	if originalName != "" && originalName != schedule.Name {
		if _, exists := e.schedules[schedule.Name]; exists {
			return ConfigError("schedule already exists: " + schedule.Name)
		}
	}

	if err := e.store.SaveSchedule(schedule); err != nil {
		return err
	}
	if originalName != "" && originalName != schedule.Name {
		if err := e.store.DeleteSchedule(originalName); err != nil {
			_ = e.store.DeleteSchedule(schedule.Name)
			_ = e.store.SaveSchedule(oldSchedule)
			return err
		}
	}

	if originalName != "" && originalName != schedule.Name {
		e.removeScheduleLocked(originalName)
	}
	e.removeScheduleLocked(schedule.Name)
	if err := e.registerSchedule(schedule); err != nil {
		e.removeScheduleLocked(schedule.Name)
		_ = e.store.DeleteSchedule(schedule.Name)
		if hadOriginal {
			_ = e.store.SaveSchedule(oldSchedule)
			_ = e.registerSchedule(oldSchedule)
		}
		return err
	}
	return nil
}

func (e *Engine) DeleteSchedule(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.schedules[name]; !ok {
		return ConfigError("schedule not found: " + name)
	}
	e.removeScheduleLocked(name)
	return e.store.DeleteSchedule(name)
}

func (e *Engine) TogglePause(name string) (paused bool, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	schedule, ok := e.schedules[name]
	if !ok {
		return false, ConfigError("schedule not found: " + name)
	}

	schedule.Paused = !schedule.Paused
	if err := e.store.SaveSchedule(schedule); err != nil {
		return false, err
	}

	if schedule.Paused {
		e.removeEntryLocked(name)
	} else {
		if err := e.registerScheduleLocked(schedule); err != nil {
			schedule.Paused = true
			_ = e.store.SaveSchedule(schedule)
			e.schedules[name] = schedule
			return true, err
		}
	}
	e.schedules[name] = schedule
	return schedule.Paused, nil
}

func (e *Engine) registerScheduleLocked(schedule ScheduleConfig) error {
	e.removeEntryLocked(schedule.Name)
	if schedule.Paused {
		return nil
	}
	if schedule.ScheduleType == "once" && schedule.CompletedAt != nil {
		return nil
	}
	scheduleCopy := schedule
	cronSchedule, err := e.cronSchedule(schedule)
	if err != nil {
		return err
	}
	entryID, err := e.cron.ScheduleJob(cronSchedule, cron.FuncJob(func() {
		scheduledAt := scheduledAtForSchedule(scheduleCopy, time.Now())
		e.Trigger(context.Background(), scheduleCopy.Name, "scheduler", scheduledAt)
	}))
	if err != nil {
		return err
	}
	e.entryIDs[schedule.Name] = entryID
	return nil
}

func (e *Engine) removeScheduleLocked(name string) {
	e.removeEntryLocked(name)
	delete(e.schedules, name)
}

func (e *Engine) removeEntryLocked(name string) {
	if entryID, ok := e.entryIDs[name]; ok {
		e.cron.Remove(entryID)
		delete(e.entryIDs, name)
	}
}

func (e *Engine) Start() error {
	e.cron.Start()
	return nil
}

func (e *Engine) Stop() {
	ctx := e.cron.Stop()
	<-ctx.Done()
	e.wg.Wait()
}

func (e *Engine) Schedules() []ScheduleConfig {
	e.mu.RLock()
	defer e.mu.RUnlock()

	schedules := make([]ScheduleConfig, 0, len(e.schedules))
	for _, schedule := range e.schedules {
		if entryID, ok := e.entryIDs[schedule.Name]; ok {
			entry := e.cron.Entry(entryID)
			schedule.NextRun = entry.Next
			schedule.PreviousRun = entry.Prev
		}
		schedules = append(schedules, schedule)
	}
	slices.SortFunc(schedules, func(a, b ScheduleConfig) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return schedules
}

func (e *Engine) FaktoryInstanceNames() []string {
	return e.cfg.FaktoryInstanceNames()
}

func (e *Engine) FaktoryQueues(ctx context.Context, instanceName string) ([]string, error) {
	inst, ok := e.cfg.FindFaktoryInstance(instanceName)
	if !ok {
		return nil, ConfigError("unknown faktory instance: " + instanceName)
	}
	return faktoryQueues(ctx, inst)
}

func (e *Engine) Runs(limit int) ([]Run, error) {
	return e.store.ListRuns(limit)
}

func (e *Engine) ClearRuns() error {
	return e.store.ClearRuns()
}

func (e *Engine) TriggerManual(ctx context.Context, name string) (Run, error) {
	e.mu.RLock()
	schedule, ok := e.schedules[name]
	e.mu.RUnlock()
	if !ok {
		return Run{}, ConfigError("schedule not found: " + name)
	}
	if schedule.Paused {
		return Run{}, ConfigError("cannot run paused job: " + name)
	}
	return e.Trigger(ctx, name, "manual", time.Now().UTC())
}

func (e *Engine) Trigger(ctx context.Context, name string, triggeredBy string, scheduledAt time.Time) (Run, error) {
	e.mu.RLock()
	schedule, ok := e.schedules[name]
	e.mu.RUnlock()
	if !ok {
		return Run{}, ConfigError("schedule not found: " + name)
	}

	if schedule.ConcurrencyPolicy == "forbid" {
		running, err := e.store.HasRunningRun(schedule.Name)
		if err != nil {
			return Run{}, err
		}
		if running {
			run := Run{
				ID:           runID(schedule.Name, triggeredBy, scheduledAt),
				ScheduleName: schedule.Name,
				TargetType:   schedule.Target.Type,
				TriggeredBy:  triggeredBy,
				Status:       RunSkipped,
				ScheduledAt:  scheduledAt,
				StartedAt:    time.Now().UTC(),
				Error:        "skipped because a previous run is still running",
			}
			finished := time.Now().UTC()
			run.FinishedAt = &finished
			if err := e.store.SaveRun(run); err != nil {
				return Run{}, err
			}
			return run, nil
		}
	}

	run := Run{
		ID:           runID(schedule.Name, triggeredBy, scheduledAt),
		ScheduleName: schedule.Name,
		TargetType:   schedule.Target.Type,
		TriggeredBy:  triggeredBy,
		Status:       RunRunning,
		ScheduledAt:  scheduledAt,
		StartedAt:    time.Now().UTC(),
	}
	if err := e.store.SaveRun(run); err != nil {
		return Run{}, err
	}
	if schedule.ScheduleType == "once" && triggeredBy == "scheduler" {
		if err := e.markOneTimeCompleted(schedule.Name); err != nil {
			e.logger.Error("mark one-time schedule complete", "schedule", schedule.Name, "run_id", run.ID, "error", err)
		}
	}

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.executeRun(schedule, run)
	}()
	return run, nil
}

func (e *Engine) markOneTimeCompleted(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	schedule, ok := e.schedules[name]
	if !ok {
		return ConfigError("schedule not found: " + name)
	}
	if schedule.ScheduleType != "once" || schedule.CompletedAt != nil {
		return nil
	}
	now := time.Now().UTC()
	schedule.CompletedAt = &now
	if err := e.store.SaveSchedule(schedule); err != nil {
		return err
	}
	e.schedules[name] = schedule
	e.removeEntryLocked(name)
	return nil
}

func (e *Engine) executeRun(schedule ScheduleConfig, run Run) {
	ctx, cancel := context.WithTimeout(context.Background(), schedule.Timeout.Duration)
	defer cancel()

	result, err := TriggerTarget(ctx, schedule, run, e.cfg.Faktory)
	finished := time.Now().UTC()
	run.FinishedAt = &finished
	run.StructuredOutput = result.StructuredOutput
	if err != nil {
		run.Status = RunFailed
		run.Error = err.Error()
		e.logger.Warn("run failed", "schedule", run.ScheduleName, "run_id", run.ID, "error", err)
	} else {
		run.Status = RunSucceeded
		e.logger.Info("run succeeded", "schedule", run.ScheduleName, "run_id", run.ID)
	}
	if err := e.store.UpdateRun(run); err != nil {
		e.logger.Error("update run", "run_id", run.ID, "error", err)
	}
}

func runID(scheduleName, triggeredBy string, scheduledAt time.Time) string {
	sum := sha1.Sum([]byte(scheduleName + "|" + triggeredBy + "|" + scheduledAt.UTC().Format(time.RFC3339Nano)))
	return scheduleName + "-" + hex.EncodeToString(sum[:])[:12]
}

type rateSchedule struct {
	start    time.Time
	interval time.Duration
}

func (s rateSchedule) Next(t time.Time) time.Time {
	if s.interval <= 0 {
		return time.Time{}
	}
	if t.Before(s.start) {
		return s.start
	}
	elapsed := t.Sub(s.start)
	intervals := elapsed/s.interval + 1
	return s.start.Add(intervals * s.interval)
}

type onceSchedule struct {
	runAt time.Time
}

func (s onceSchedule) Next(t time.Time) time.Time {
	if s.runAt.After(t) {
		return s.runAt
	}
	return time.Time{}
}
