package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfigAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clock-relay.yaml")
	raw := []byte(`
schedules:
  - name: heartbeat
    cron: "*/5 * * * *"
    target:
      type: http
      url: http://localhost:3000/heartbeat
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != ":9808" {
		t.Fatalf("expected default addr, got %s", cfg.Server.Addr)
	}
	if cfg.Store.Type != "bbolt" {
		t.Fatalf("expected default store, got %s", cfg.Store.Type)
	}
	if cfg.Schedules[0].Timeout.Duration != 30*time.Second {
		t.Fatalf("expected default timeout, got %s", cfg.Schedules[0].Timeout)
	}
	if cfg.RunRetention.MaxRecords != 10_000 {
		t.Fatalf("expected default run retention max records, got %d", cfg.RunRetention.MaxRecords)
	}
	if cfg.RunRetention.MaxAgeDays != 30 {
		t.Fatalf("expected default run retention max age days, got %d", cfg.RunRetention.MaxAgeDays)
	}
	if cfg.RunLogging.Stdout != "summary" {
		t.Fatalf("expected default run logging stdout summary, got %q", cfg.RunLogging.Stdout)
	}
}

func TestLoadConfigAcceptsRunRetention(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clock-relay.yaml")
	raw := []byte(`
run_retention:
  max_records: 500
  max_age_days: 14

schedules: []
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RunRetention.MaxRecords != 500 {
		t.Fatalf("expected configured max records, got %d", cfg.RunRetention.MaxRecords)
	}
	if cfg.RunRetention.MaxAgeDays != 14 {
		t.Fatalf("expected configured max age days, got %d", cfg.RunRetention.MaxAgeDays)
	}
}

func TestConfigRejectsInvalidRunRetention(t *testing.T) {
	cfg := Config{
		RunRetention: RunRetentionConfig{MaxRecords: -1},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid run retention validation error")
	}
}

func TestLoadConfigAcceptsRunLogging(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clock-relay.yaml")
	raw := []byte(`
run_logging:
  stdout: full

schedules: []
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RunLogging.Stdout != "full" {
		t.Fatalf("expected configured run logging stdout, got %q", cfg.RunLogging.Stdout)
	}
}

func TestConfigRejectsInvalidRunLogging(t *testing.T) {
	cfg := Config{
		RunLogging: RunLoggingConfig{Stdout: "verbose"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid run logging validation error")
	}
}

func TestConfigAcceptsAllowConcurrentRuns(t *testing.T) {
	cfg := Config{
		Schedules: []ScheduleConfig{
			{
				Name:            "concurrent",
				Cron:            "* * * * *",
				Timezone:        "UTC",
				Timeout:         Duration{time.Second},
				AllowConcurrent: true,
				Target:          TargetConfig{Type: "http", URL: "http://localhost:3000/a"},
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	NormalizeSchedule(&cfg.Schedules[0])
	if !cfg.Schedules[0].AllowsConcurrentRuns() {
		t.Fatal("expected allow_concurrent_runs to be true")
	}
}

func TestBundledConfigsStartWithoutSeedSchedules(t *testing.T) {
	for _, path := range []string{
		"../../clock-relay.example.yaml",
		"../../examples/faktory/clock-relay.yaml",
	} {
		t.Run(path, func(t *testing.T) {
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.Schedules) != 0 {
				t.Fatalf("expected no seed schedules, got %#v", cfg.Schedules)
			}
		})
	}
}

func TestConfigRejectsDuplicateSchedules(t *testing.T) {
	cfg := Config{
		Schedules: []ScheduleConfig{
			{Name: "same", Cron: "* * * * *", Timezone: "UTC", Timeout: Duration{time.Second}, Target: TargetConfig{Type: "http", URL: "http://localhost:3000/a"}},
			{Name: "same", Cron: "* * * * *", Timezone: "UTC", Timeout: Duration{time.Second}, Target: TargetConfig{Type: "http", URL: "http://localhost:3000/b"}},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected duplicate schedule validation error")
	}
}

func TestConfigAcceptsFaktoryTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clock-relay.yaml")
	raw := []byte(`
faktory:
  - name: default
    url: tcp://faktory:7419

schedules:
  - name: faktory-smoke
    cron: "*/5 * * * *"
    target:
      type: faktory
      instance: default
      job_type: smoke_job
      args:
        - account_id: acct_123
        - 42
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Faktory) != 1 {
		t.Fatalf("expected 1 faktory instance, got %d", len(cfg.Faktory))
	}
	if cfg.Faktory[0].Name != "default" {
		t.Fatalf("expected instance name, got %q", cfg.Faktory[0].Name)
	}
	target := cfg.Schedules[0].Target
	if target.Instance != "default" {
		t.Fatalf("expected instance reference, got %q", target.Instance)
	}
	if target.Queue != "default" {
		t.Fatalf("expected default queue, got %q", target.Queue)
	}
	if target.JobType != "smoke_job" {
		t.Fatalf("expected job type, got %q", target.JobType)
	}
	if len(target.Args) != 2 {
		t.Fatalf("expected 2 args, got %#v", target.Args)
	}
}

func TestConfigRejectsUnknownFaktoryInstance(t *testing.T) {
	cfg := Config{
		Schedules: []ScheduleConfig{
			{Name: "bad-ref", Cron: "* * * * *", Timezone: "UTC", Timeout: Duration{time.Second}, Target: TargetConfig{Type: "faktory", JobType: "test", Instance: "nonexistent"}},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected unknown instance validation error")
	}
}

func TestConfigRejectsDuplicateFaktoryInstance(t *testing.T) {
	cfg := Config{
		Faktory: []FaktoryInstance{
			{Name: "dup", URL: "tcp://a:7419"},
			{Name: "dup", URL: "tcp://b:7419"},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected duplicate instance validation error")
	}
}

func TestConfigAcceptsFaktoryWithPasswordEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clock-relay.yaml")
	raw := []byte(`
faktory:
  - name: prod
    url: tcp://faktory:7419
    password_env: FAKTORY_PASSWORD

schedules:
  - name: prod-job
    cron: "0 * * * *"
    target:
      type: faktory
      instance: prod
      job_type: nightly_sync
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Faktory[0].PasswordEnv != "FAKTORY_PASSWORD" {
		t.Fatalf("expected password_env, got %q", cfg.Faktory[0].PasswordEnv)
	}
}

func TestConfigRejectsFaktoryWithoutInstance(t *testing.T) {
	cfg := Config{
		Schedules: []ScheduleConfig{
			{Name: "no-instance", Cron: "* * * * *", Timezone: "UTC", Timeout: Duration{time.Second}, Target: TargetConfig{Type: "faktory", JobType: "test"}},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing instance validation error")
	}
}

func TestConfigAcceptsExplicitScheduleTypes(t *testing.T) {
	cfg := Config{
		Schedules: []ScheduleConfig{
			{
				Name:         "maintenance",
				ScheduleType: "once",
				RunAt:        "2026-05-10T09:00",
				Timezone:     "America/Chicago",
				Timeout:      Duration{time.Second},
				Target:       TargetConfig{Type: "http", URL: "http://localhost:3000/maintenance"},
			},
			{
				Name:         "heartbeat",
				ScheduleType: "rate",
				StartsAt:     "2026-05-08T10:30",
				Timezone:     "UTC",
				RateInterval: 15,
				RateUnit:     "minutes",
				Timeout:      Duration{time.Second},
				Target:       TargetConfig{Type: "http", URL: "http://localhost:3000/heartbeat"},
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestConfigRejectsInvalidRateSchedule(t *testing.T) {
	cfg := Config{
		Schedules: []ScheduleConfig{
			{
				Name:         "bad-rate",
				ScheduleType: "rate",
				StartsAt:     "2026-05-08T10:30",
				Timezone:     "UTC",
				RateInterval: 0,
				RateUnit:     "minutes",
				Timeout:      Duration{time.Second},
				Target:       TargetConfig{Type: "http", URL: "http://localhost:3000/bad"},
			},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid rate schedule error")
	}
}

func TestConfigRejectsFaktoryWithoutJobType(t *testing.T) {
	cfg := Config{
		Faktory: []FaktoryInstance{{Name: "default", URL: "tcp://faktory:7419"}},
		Schedules: []ScheduleConfig{
			{Name: "missing-job-type", Cron: "* * * * *", Timezone: "UTC", Timeout: Duration{time.Second}, Target: TargetConfig{Type: "faktory", Instance: "default"}},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing job_type validation error")
	}
}
