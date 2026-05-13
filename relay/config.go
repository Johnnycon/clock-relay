package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig      `yaml:"server" json:"server"`
	Store     StoreConfig       `yaml:"store" json:"store"`
	Faktory   []FaktoryInstance `yaml:"faktory,omitempty" json:"-"`
	Schedules []ScheduleConfig  `yaml:"schedules" json:"schedules"`
}

type FaktoryInstance struct {
	Name        string `yaml:"name" json:"name"`
	URL         string `yaml:"url" json:"url"`
	PasswordEnv string `yaml:"password_env,omitempty" json:"-"`
}

type ServerConfig struct {
	Addr string `yaml:"addr" json:"addr"`
}

type StoreConfig struct {
	Type string `yaml:"type" json:"type"`
	Path string `yaml:"path" json:"path"`
}

type ScheduleConfig struct {
	Name              string       `yaml:"name" json:"name"`
	Description       string       `yaml:"description" json:"description"`
	ScheduleType      string       `yaml:"schedule_type" json:"schedule_type"`
	Cron              string       `yaml:"cron" json:"cron"`
	Timezone          string       `yaml:"timezone" json:"timezone"`
	RunAt             string       `yaml:"run_at,omitempty" json:"run_at,omitempty"`
	StartsAt          string       `yaml:"starts_at,omitempty" json:"starts_at,omitempty"`
	RateInterval      int          `yaml:"rate_interval,omitempty" json:"rate_interval,omitempty"`
	RateUnit          string       `yaml:"rate_unit,omitempty" json:"rate_unit,omitempty"`
	CompletedAt       *time.Time   `yaml:"completed_at,omitempty" json:"completed_at,omitempty"`
	Paused            bool         `yaml:"paused,omitempty" json:"paused,omitempty"`
	Timeout           Duration     `yaml:"timeout" json:"timeout"`
	ConcurrencyPolicy string       `yaml:"concurrency_policy" json:"concurrency_policy"`
	Target            TargetConfig `yaml:"target" json:"target"`
	NextRun           time.Time    `yaml:"-" json:"next_run,omitzero"`
	PreviousRun       time.Time    `yaml:"-" json:"previous_run,omitzero"`
}

type TargetConfig struct {
	Type     string            `yaml:"type" json:"type"`
	URL      string            `yaml:"url,omitempty" json:"url,omitempty"`
	Method   string            `yaml:"method,omitempty" json:"method,omitempty"`
	Headers  map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	Body     map[string]any    `yaml:"body,omitempty" json:"body,omitempty"`
	Args     []any             `yaml:"args,omitempty" json:"args,omitempty"`
	Queue    string            `yaml:"queue,omitempty" json:"queue,omitempty"`
	JobType  string            `yaml:"job_type,omitempty" json:"job_type,omitempty"`
	Instance string            `yaml:"instance,omitempty" json:"instance,omitempty"`
}

type Duration struct {
	time.Duration
}

type ConfigError string

func (e ConfigError) Error() string {
	return string(e)
}

func LoadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, err
	}
	cfg.applyDefaults()
	return cfg, cfg.Validate()
}

func NormalizeSchedule(schedule *ScheduleConfig) {
	if schedule.ScheduleType == "" {
		schedule.ScheduleType = "cron"
	}
	if schedule.Timezone == "" {
		schedule.Timezone = "UTC"
	}
	if schedule.Timeout.Duration == 0 {
		schedule.Timeout.Duration = 30 * time.Second
	}
	if schedule.ConcurrencyPolicy == "" {
		schedule.ConcurrencyPolicy = "forbid"
	}
	if schedule.Target.Method == "" {
		schedule.Target.Method = "POST"
	}
	if schedule.Target.Type == "faktory" && schedule.Target.Queue == "" {
		schedule.Target.Queue = "default"
	}
	schedule.NextRun = time.Time{}
	schedule.PreviousRun = time.Time{}
}

func (c *Config) applyDefaults() {
	if c.Server.Addr == "" {
		c.Server.Addr = ":9808"
	}
	if c.Store.Type == "" {
		c.Store.Type = "bbolt"
	}
	if c.Store.Path == "" {
		c.Store.Path = "./data/clock-relay.db"
	}
	for i := range c.Schedules {
		NormalizeSchedule(&c.Schedules[i])
	}
}

func (c Config) Validate() error {
	seenInstances := map[string]struct{}{}
	for _, inst := range c.Faktory {
		if inst.Name == "" {
			return ConfigError("faktory instance name is required")
		}
		if _, ok := seenInstances[inst.Name]; ok {
			return ConfigError("duplicate faktory instance: " + inst.Name)
		}
		seenInstances[inst.Name] = struct{}{}
		if inst.URL == "" {
			return ConfigError("faktory instance url is required for: " + inst.Name)
		}
	}

	seen := map[string]struct{}{}
	for _, schedule := range c.Schedules {
		NormalizeSchedule(&schedule)
		if schedule.Name == "" {
			return ConfigError("schedule name is required")
		}
		if !validScheduleName.MatchString(schedule.Name) || strings.Contains(schedule.Name, "..") {
			return ConfigError("schedule name may only contain letters, numbers, dots, underscores, and dashes: " + schedule.Name)
		}
		if _, ok := seen[schedule.Name]; ok {
			return ConfigError("duplicate schedule name: " + schedule.Name)
		}
		seen[schedule.Name] = struct{}{}
		if _, err := time.LoadLocation(schedule.Timezone); err != nil {
			return fmt.Errorf("invalid timezone for schedule %s: %w", schedule.Name, err)
		}
		if err := validateScheduleTiming(schedule); err != nil {
			return err
		}
		switch schedule.ConcurrencyPolicy {
		case "allow", "forbid":
		default:
			return ConfigError("unsupported concurrency_policy for schedule " + schedule.Name + ": " + schedule.ConcurrencyPolicy)
		}
		if err := schedule.Target.Validate(schedule.Name); err != nil {
			return err
		}
		if schedule.Target.Type == "faktory" && schedule.Target.Instance != "" {
			if _, ok := seenInstances[schedule.Target.Instance]; !ok {
				return ConfigError("unknown faktory instance for schedule " + schedule.Name + ": " + schedule.Target.Instance)
			}
		}
	}
	return nil
}

func (c Config) FaktoryInstanceNames() []string {
	names := make([]string, len(c.Faktory))
	for i, inst := range c.Faktory {
		names[i] = inst.Name
	}
	return names
}

func (c Config) FindFaktoryInstance(name string) (FaktoryInstance, bool) {
	for _, inst := range c.Faktory {
		if inst.Name == name {
			return inst, true
		}
	}
	return FaktoryInstance{}, false
}

var validScheduleName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func validateScheduleTiming(schedule ScheduleConfig) error {
	switch schedule.ScheduleType {
	case "once":
		if schedule.RunAt == "" {
			return ConfigError("run_at is required for once schedule: " + schedule.Name)
		}
		if _, err := parseLocalDateTime(schedule.RunAt, schedule.Timezone); err != nil {
			return fmt.Errorf("invalid run_at for schedule %s: %w", schedule.Name, err)
		}
	case "rate":
		if schedule.StartsAt == "" {
			return ConfigError("starts_at is required for rate schedule: " + schedule.Name)
		}
		if _, err := parseLocalDateTime(schedule.StartsAt, schedule.Timezone); err != nil {
			return fmt.Errorf("invalid starts_at for schedule %s: %w", schedule.Name, err)
		}
		if schedule.RateInterval <= 0 {
			return ConfigError("rate_interval must be positive for schedule: " + schedule.Name)
		}
		switch schedule.RateUnit {
		case "minutes", "hours", "days":
		default:
			return ConfigError("rate_unit must be minutes, hours, or days for schedule: " + schedule.Name)
		}
	case "cron":
		if schedule.Cron == "" {
			return ConfigError("cron is required for schedule: " + schedule.Name)
		}
	default:
		return ConfigError("unsupported schedule_type for schedule " + schedule.Name + ": " + schedule.ScheduleType)
	}
	return nil
}

func parseLocalDateTime(raw string, timezone string) (time.Time, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, err
	}
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05"} {
		parsed, err := time.ParseInLocation(layout, raw, location)
		if err == nil {
			return parsed, nil
		}
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err == nil {
		return parsed.In(location), nil
	}
	return time.Time{}, fmt.Errorf("expected local datetime like 2026-05-10T09:00")
}

func (t TargetConfig) Validate(scheduleName string) error {
	switch t.Type {
	case "http":
		if t.URL == "" {
			return ConfigError("http target url is required for schedule: " + scheduleName)
		}
	case "faktory":
		if t.Instance == "" {
			return ConfigError("faktory target instance is required for schedule: " + scheduleName)
		}
		if t.JobType == "" {
			return ConfigError("faktory target job_type is required for schedule: " + scheduleName)
		}
	default:
		return ConfigError("unsupported target type for schedule " + scheduleName + ": " + t.Type)
	}
	return nil
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var raw string
	if err := value.Decode(&raw); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return err
	}
	d.Duration = parsed
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%q", d.String())), nil
}

func (d *Duration) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if value == "" {
		d.Duration = 0
		return nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return err
	}
	d.Duration = parsed
	return nil
}

func (d Duration) String() string {
	if d.Duration == 0 {
		return ""
	}
	return d.Duration.String()
}

func IsConfigNotFound(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}
