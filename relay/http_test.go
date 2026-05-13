package relay

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestScheduleFromRequestParsesFaktoryForm(t *testing.T) {
	form := url.Values{
		"name":                    {"faktory-smoke"},
		"cron":                    {"*/5 * * * *"},
		"timezone":                {"UTC"},
		"timeout":                 {"10s"},
		"allow_concurrent_runs":   {"false"},
		"target_type":             {"faktory"},
		"target_faktory_instance": {"default"},
		"target_faktory_queue":    {"critical"},
		"target_faktory_job_type": {"smoke_job"},
		"target_faktory_args":     {`[{"account_id":"acct_123"},42]`},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/schedules", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	schedule, _, err := scheduleFromRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if schedule.Target.Type != "faktory" {
		t.Fatalf("expected faktory target, got %q", schedule.Target.Type)
	}
	if schedule.Target.Instance != "default" {
		t.Fatalf("expected faktory instance, got %q", schedule.Target.Instance)
	}
	if schedule.Target.Queue != "critical" {
		t.Fatalf("expected queue, got %q", schedule.Target.Queue)
	}
	if schedule.Target.JobType != "smoke_job" {
		t.Fatalf("expected job type, got %q", schedule.Target.JobType)
	}
	if len(schedule.Target.Args) != 2 {
		t.Fatalf("expected 2 args, got %#v", schedule.Target.Args)
	}
	arg, ok := schedule.Target.Args[0].(map[string]any)
	if !ok || arg["account_id"] != "acct_123" {
		t.Fatalf("expected structured arg, got %#v", schedule.Target.Args[0])
	}
}

func TestScheduleFromRequestParsesRateForm(t *testing.T) {
	form := url.Values{
		"name":                  {"heartbeat"},
		"schedule_type":         {"rate"},
		"starts_at":             {"2026-05-08T10:30"},
		"timezone":              {"America/Chicago"},
		"rate_interval":         {"15"},
		"rate_unit":             {"minutes"},
		"timeout":               {"10s"},
		"allow_concurrent_runs": {"true"},
		"target_type":           {"http"},
		"target_url":            {"http://localhost:3000/heartbeat"},
		"target_method":         {"POST"},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/schedules", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	schedule, _, err := scheduleFromRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if schedule.ScheduleType != "rate" {
		t.Fatalf("expected rate schedule, got %q", schedule.ScheduleType)
	}
	if schedule.StartsAt != "2026-05-08T10:30" || schedule.RateInterval != 15 || schedule.RateUnit != "minutes" {
		t.Fatalf("unexpected rate fields: %#v", schedule)
	}
	if !schedule.AllowsConcurrentRuns() {
		t.Fatal("expected allow_concurrent_runs form value to be parsed")
	}
}

func TestScheduleFromRequestParsesOnceForm(t *testing.T) {
	form := url.Values{
		"name":                  {"maintenance"},
		"schedule_type":         {"once"},
		"run_at":                {"2026-05-10T09:00"},
		"timezone":              {"America/Chicago"},
		"timeout":               {"10s"},
		"allow_concurrent_runs": {"false"},
		"target_type":           {"http"},
		"target_url":            {"http://localhost:3000/maintenance"},
		"target_method":         {"POST"},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/schedules", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	schedule, _, err := scheduleFromRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if schedule.ScheduleType != "once" || schedule.RunAt != "2026-05-10T09:00" {
		t.Fatalf("unexpected once fields: %#v", schedule)
	}
}

func TestScheduleFromRequestRejectsInvalidFaktoryArgs(t *testing.T) {
	form := url.Values{
		"name":                    {"faktory-smoke"},
		"cron":                    {"*/5 * * * *"},
		"timezone":                {"UTC"},
		"timeout":                 {"10s"},
		"allow_concurrent_runs":   {"false"},
		"target_type":             {"faktory"},
		"target_faktory_job_type": {"smoke_job"},
		"target_faktory_args":     {`{"account_id":"acct_123"}`},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/schedules", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if _, _, err := scheduleFromRequest(req); err == nil {
		t.Fatal("expected invalid JSON array error")
	}
}

func TestFaktoryScheduleEditFormRenders(t *testing.T) {
	cfg := Config{
		Store: StoreConfig{Type: "memory"},
		Faktory: []FaktoryInstance{
			{Name: "default", URL: "tcp://faktory:7419"},
		},
	}
	engine, err := NewEngine(cfg, NewMemoryStore(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	schedule := ScheduleConfig{
		Name:     "faktory-smoke",
		Cron:     "*/5 * * * *",
		Timezone: "UTC",
		Timeout:  Duration{Duration: 10 * time.Second},
		Target: TargetConfig{
			Type:     "faktory",
			Instance: "default",
			Queue:    "default",
			JobType:  "smoke_job",
			Args:     []any{map[string]any{"account_id": "acct_123"}},
		},
	}
	if err := engine.SaveSchedule("", schedule); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/schedules/faktory-smoke/edit", nil)
	res := httptest.NewRecorder()
	NewHTTPServer(engine, slog.Default()).ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.Code)
	}
	body := res.Body.String()
	for _, want := range []string{
		`name="target_faktory_instance"`,
		`name="target_faktory_job_type"`,
		`smoke_job`,
		`value="faktory"`,
		`account_id`,
		`acct_123`,
		`wiz-narrative`,
		`data-wiz-step`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected edit form to contain %q", want)
		}
	}
}

func TestNewJobShowsTypePicker(t *testing.T) {
	engine, err := NewEngine(Config{
		Store:   StoreConfig{Type: "memory"},
		Faktory: []FaktoryInstance{{Name: "default", URL: "tcp://faktory:7419"}},
	}, NewMemoryStore(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/schedules/new", nil)
	res := httptest.NewRecorder()
	NewHTTPServer(engine, slog.Default()).ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.Code)
	}
	body := res.Body.String()
	for _, want := range []string{
		`Faktory job`,
		`Web request`,
		`Coming soon`,
		`/schedules/new?type=faktory`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected type picker to contain %q", want)
		}
	}
}

func TestNewFaktoryJobFormRenders(t *testing.T) {
	engine, err := NewEngine(Config{
		Store:   StoreConfig{Type: "memory"},
		Faktory: []FaktoryInstance{{Name: "default", URL: "tcp://faktory:7419"}},
	}, NewMemoryStore(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/schedules/new?type=faktory", nil)
	res := httptest.NewRecorder()
	NewHTTPServer(engine, slog.Default()).ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.Code)
	}
	body := res.Body.String()
	for _, want := range []string{
		`New Faktory Job`,
		`name="target_faktory_instance"`,
		`name="target_faktory_queue"`,
		`name="target_faktory_job_type"`,
		`name="target_faktory_args"`,
		`value="faktory"`,
		`wiz-narrative`,
		`data-wiz-step="job"`,
		`data-wiz-step="schedule-type"`,
		`data-wiz-step="review"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected faktory wizard to contain %q", want)
		}
	}
	if strings.Contains(body, `name="target_command"`) {
		t.Fatal("faktory wizard should not contain exec fields")
	}
}

func TestIndexIncludesLiveRefresh(t *testing.T) {
	engine, err := NewEngine(Config{Store: StoreConfig{Type: "memory"}}, NewMemoryStore(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	res := httptest.NewRecorder()
	NewHTTPServer(engine, slog.Default()).ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.Code)
	}
	body := res.Body.String()
	for _, want := range []string{
		`data-live-section="jobs"`,
		`data-live-section="runs"`,
		`id="run-output-dialog"`,
		`id="work-tz-select"`,
		`id="time-format-select"`,
		`setInterval(refreshLiveSections, 3000)`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected index to contain %q", want)
		}
	}
}

func TestTimeHelperFormatsLocalDateTimeInSelectedTimezone(t *testing.T) {
	engine, err := NewEngine(Config{Store: StoreConfig{Type: "memory"}}, NewMemoryStore(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	res := httptest.NewRecorder()
	NewHTTPServer(engine, slog.Default()).ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.Code)
	}
	body := res.Body.String()
	for _, want := range []string{
		`const localDateTime = (date, timezone) =>`,
		`timeZone: tz`,
		`formatToParts(date)`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected time helper to contain %q", want)
		}
	}
}

func TestNewFormsDefaultDatetimesInPreferredTimezone(t *testing.T) {
	schedule := ScheduleConfig{
		Name:         "http-job",
		ScheduleType: "rate",
		StartsAt:     "2026-05-08T10:30",
		Timezone:     "UTC",
		RateInterval: 5,
		RateUnit:     "minutes",
		Timeout:      Duration{Duration: 10 * time.Second},
		Target:       TargetConfig{Type: "http", URL: "http://localhost:3000/heartbeat", Method: "POST"},
	}
	store := NewMemoryStore()
	engine, err := NewEngine(Config{
		Store: StoreConfig{Type: "memory"},
		Schedules: []ScheduleConfig{
			schedule,
		},
		Faktory: []FaktoryInstance{{Name: "default", URL: "tcp://faktory:7419"}},
	}, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
	}{
		{name: "legacy form", path: "/schedules/http-job/edit"},
		{name: "faktory wizard", path: "/schedules/new?type=faktory"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			res := httptest.NewRecorder()
			NewHTTPServer(engine, slog.Default()).ServeHTTP(res, req)

			if res.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", res.Code)
			}
			body := res.Body.String()
			for _, want := range []string{
				`const preferredTimezone = ClockRelayTime.preferredScheduleTimezone();`,
				`ClockRelayTime.selectTimezone`,
				`preferredTimezone`,
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("expected form script to contain %q", want)
				}
			}
			for _, want := range []string{
				`localDateTime(now, preferredTimezone)`,
				`preferredTimezone`,
				`localDateTime(new Date(now.getTime() + 5`,
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("expected form script to pass preferred timezone to %q", want)
				}
			}
		})
	}
}

func TestScheduleSummary(t *testing.T) {
	tests := []struct {
		name     string
		cron     string
		contains string
	}{
		{name: "every minute", cron: "*/1 * * * *", contains: "Every minute"},
		{name: "every n minutes", cron: "*/5 * * * *", contains: "Every 5 minutes"},
		{name: "hourly", cron: "15 * * * *", contains: "Hourly at :15"},
		{name: "daily", cron: "30 9 * * *", contains: "Daily at"},
		{name: "weekly", cron: "0 14 * * 1", contains: "Weekly on Monday at"},
		{name: "monthly", cron: "0 8 1 * *", contains: "Monthly on the 1st at"},
		{name: "yearly", cron: "0 0 1 1 *", contains: "Every Jan 1 at"},
		{name: "fallback", cron: "5,10 * * * *", contains: "Cron: 5,10 * * * *"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(scheduleSummary(ScheduleConfig{Cron: tt.cron}))
			if !strings.Contains(got, tt.contains) {
				t.Fatalf("expected summary to contain %q, got %q", tt.contains, got)
			}
		})
	}

	t.Run("daily emits wall-time span", func(t *testing.T) {
		got := string(scheduleSummary(ScheduleConfig{Cron: "30 9 * * *", Timezone: "UTC"}))
		if !strings.Contains(got, `class="wall-time"`) || !strings.Contains(got, `data-hour="9"`) || !strings.Contains(got, `data-minute="30"`) {
			t.Fatalf("expected wall-time span for cron summary, got %q", got)
		}
	})
}

func TestScheduleDetailsAvoidsInternalTypeLabels(t *testing.T) {
	got := string(scheduleDetails(ScheduleConfig{
		ScheduleType: "rate",
		StartsAt:     "2026-05-08T18:39",
		Timezone:     "America/Chicago",
	}))
	if !strings.Contains(got, "Starts") || !strings.Contains(got, "No overlap") {
		t.Fatalf("expected starts and concurrency info, got %q", got)
	}
	if !strings.Contains(got, `class="local-time"`) {
		t.Fatalf("expected local-time span for StartsAt, got %q", got)
	}
	if strings.Contains(got, "rate") || strings.Contains(got, "forbid") {
		t.Fatalf("expected operator-facing details, got %q", got)
	}
	if !strings.Contains(got, `class="timezone-context"`) || !strings.Contains(got, `data-source-tz="America/Chicago"`) {
		t.Fatalf("expected deferred timezone context, got %q", got)
	}
}

func TestNextRunLabel(t *testing.T) {
	next := time.Date(2026, 5, 8, 12, 30, 0, 0, time.UTC)
	got := string(nextRunLabel(ScheduleConfig{NextRun: next, Timezone: "America/Chicago"}))
	if !strings.Contains(got, `data-time="2026-05-08T12:30:00Z"`) {
		t.Fatalf("expected data-time attribute with UTC time, got %q", got)
	}
	if !strings.Contains(got, `data-source-tz="America/Chicago"`) {
		t.Fatalf("expected data-source-tz attribute, got %q", got)
	}

	completedAt := time.Now().UTC()
	if got := string(nextRunLabel(ScheduleConfig{ScheduleType: "once", CompletedAt: &completedAt})); !strings.Contains(got, "completed") {
		t.Fatalf("expected completed label, got %q", got)
	}

	if got := string(nextRunLabel(ScheduleConfig{ScheduleType: "once"})); !strings.Contains(got, "no upcoming run") {
		t.Fatalf("expected no upcoming run label, got %q", got)
	}
}

func TestRunViewsUseScheduleTimezone(t *testing.T) {
	startedAt := time.Date(2026, 5, 8, 12, 30, 0, 0, time.UTC)
	views := runViews(
		[]Run{{ScheduleName: "daily-report", StartedAt: startedAt}},
		[]ScheduleConfig{{Name: "daily-report", Timezone: "America/Chicago"}},
	)
	if len(views) != 1 {
		t.Fatalf("expected one run view, got %d", len(views))
	}
	if views[0].DisplayTimezone != "America/Chicago" {
		t.Fatalf("expected schedule timezone, got %q", views[0].DisplayTimezone)
	}
	got := string(displayRunTime(views[0]))
	if !strings.Contains(got, `data-time="2026-05-08T12:30:00Z"`) {
		t.Fatalf("expected data-time attribute, got %q", got)
	}
	if !strings.Contains(got, `data-source-tz="America/Chicago"`) {
		t.Fatalf("expected data-source-tz attribute, got %q", got)
	}
}
