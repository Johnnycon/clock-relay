package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/johnnycon/clock-relay/internal/config"
	enginepkg "github.com/johnnycon/clock-relay/internal/engine"
	"github.com/johnnycon/clock-relay/internal/model"
)

type HTTPServer struct {
	engine *enginepkg.Engine
	logger *slog.Logger
	tpl    *template.Template
}

type runView struct {
	model.Run
	DisplayTimezone string
}

func NewHTTPServer(engine *enginepkg.Engine, logger *slog.Logger) http.Handler {
	server := &HTTPServer{
		engine: engine,
		logger: logger,
		tpl: template.Must(template.New("ui").Funcs(template.FuncMap{
			"pathEscape":      url.PathEscape,
			"keyValues":       keyValues,
			"rawOutput":       rawOutput,
			"jsonArgs":        jsonArgs,
			"targetJSONArgs":  targetJSONArgs,
			"scheduleSummary": scheduleSummary,
			"scheduleDetails": scheduleDetails,
			"nextRunLabel":    nextRunLabel,
			"displayTime":     displayTime,
			"displayRunTime":  displayRunTime,
			"allowConcurrent": func(schedule config.ScheduleConfig) bool {
				return schedule.AllowsConcurrentRuns()
			},
			"uiEditableTarget": func(targetType string) bool {
				return targetType == "http" || targetType == "faktory"
			},
		}).Parse(uiHTML)),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", server.index)
	mux.HandleFunc("GET /schedules/new", server.newSchedule)
	mux.HandleFunc("GET /schedules/", server.editSchedule)
	mux.HandleFunc("GET /healthz", server.healthz)
	mux.HandleFunc("GET /v1/faktory/queues", server.apiFaktoryQueues)
	mux.HandleFunc("GET /v1/schedules", server.apiSchedules)
	mux.HandleFunc("POST /v1/schedules", server.apiSaveSchedule)
	mux.HandleFunc("GET /v1/runs", server.apiRuns)
	mux.HandleFunc("POST /v1/runs/clear", server.apiClearRuns)
	mux.HandleFunc("POST /v1/schedules/", server.apiScheduleAction)
	return mux
}

func (s *HTTPServer) index(w http.ResponseWriter, r *http.Request) {
	schedules := s.engine.Schedules()
	jobFilter := r.URL.Query().Get("job")

	runs, err := s.engine.Runs(100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if jobFilter != "" {
		filtered := runs[:0]
		for _, run := range runs {
			if run.ScheduleName == jobFilter {
				filtered = append(filtered, run)
			}
		}
		runs = filtered
	}

	data := struct {
		Schedules []config.ScheduleConfig
		Runs      []runView
		Timezones []string
		JobFilter string
		Notice    string
		Error     string
	}{
		Schedules: schedules,
		Runs:      runViews(runs, schedules),
		Timezones: timezoneOptions,
		JobFilter: jobFilter,
		Notice:    r.URL.Query().Get("notice"),
		Error:     r.URL.Query().Get("error"),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tpl.ExecuteTemplate(w, "index", data); err != nil {
		s.logger.Error("render index", "error", err)
	}
}

func runViews(runs []model.Run, schedules []config.ScheduleConfig) []runView {
	timezones := map[string]string{}
	for _, schedule := range schedules {
		timezones[schedule.Name] = schedule.Timezone
	}
	views := make([]runView, 0, len(runs))
	for _, run := range runs {
		timezone := timezones[run.ScheduleName]
		if timezone == "" {
			timezone = "UTC"
		}
		views = append(views, runView{Run: run, DisplayTimezone: timezone})
	}
	return views
}

func (s *HTTPServer) newSchedule(w http.ResponseWriter, r *http.Request) {
	targetType := r.URL.Query().Get("type")
	switch targetType {
	case "faktory":
		instances := s.engine.FaktoryInstanceNames()
		schedule := config.ScheduleConfig{
			ScheduleType:    "rate",
			Cron:            "*/5 * * * *",
			Timezone:        "UTC",
			RateInterval:    5,
			RateUnit:        "minutes",
			Timeout:         config.Duration{Duration: 5 * time.Second},
			AllowConcurrent: true,
			Target:          config.TargetConfig{Type: "faktory"},
		}
		if len(instances) == 1 {
			schedule.Target.Instance = instances[0]
		}
		s.renderScheduleForm(w, r, "New Faktory Job", "Add job", "", schedule)
	default:
		s.renderNewJobPicker(w, r)
	}
}

func (s *HTTPServer) renderNewJobPicker(w http.ResponseWriter, r *http.Request) {
	data := struct {
		FaktoryAvailable bool
	}{
		FaktoryAvailable: len(s.engine.FaktoryInstanceNames()) > 0,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tpl.ExecuteTemplate(w, "new_job_picker", data); err != nil {
		s.logger.Error("render new job picker", "error", err)
	}
}

func (s *HTTPServer) editSchedule(w http.ResponseWriter, r *http.Request) {
	name, ok := editScheduleNameFromPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	schedule, ok := s.findSchedule(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if schedule.Target.Type != "http" && schedule.Target.Type != "faktory" {
		http.Redirect(w, r, "/?error="+url.QueryEscape("target type "+schedule.Target.Type+" is not editable in the UI yet"), http.StatusSeeOther)
		return
	}
	s.renderScheduleForm(w, r, "Edit Job", "Save job", name, schedule)
}

func (s *HTTPServer) renderScheduleForm(w http.ResponseWriter, r *http.Request, title string, submitLabel string, originalName string, schedule config.ScheduleConfig) {
	data := struct {
		Title            string
		SubmitLabel      string
		OriginalName     string
		IsNew            bool
		Schedule         config.ScheduleConfig
		Timezones        []string
		FaktoryInstances []string
		Error            string
	}{
		Title:            title,
		SubmitLabel:      submitLabel,
		OriginalName:     originalName,
		IsNew:            originalName == "",
		Schedule:         schedule,
		Timezones:        timezonesWithCurrent(schedule.Timezone),
		FaktoryInstances: s.engine.FaktoryInstanceNames(),
		Error:            r.URL.Query().Get("error"),
	}
	templateName := "schedule_form"
	if schedule.Target.Type == "faktory" {
		templateName = "faktory_form"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tpl.ExecuteTemplate(w, templateName, data); err != nil {
		s.logger.Error("render schedule form", "error", err)
	}
}

func (s *HTTPServer) findSchedule(name string) (config.ScheduleConfig, bool) {
	for _, schedule := range s.engine.Schedules() {
		if schedule.Name == name {
			return schedule, true
		}
	}
	return config.ScheduleConfig{}, false
}

func (s *HTTPServer) healthz(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("ok\n"))
}

func (s *HTTPServer) apiSchedules(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.engine.Schedules())
}

func (s *HTTPServer) apiSaveSchedule(w http.ResponseWriter, r *http.Request) {
	schedule, originalName, err := scheduleFromRequest(r)
	if err != nil {
		s.respondScheduleError(w, r, err)
		return
	}
	if err := s.engine.SaveSchedule(originalName, schedule); err != nil {
		s.respondScheduleError(w, r, err)
		return
	}
	if wantsHTML(r) {
		http.Redirect(w, r, "/?notice=job+saved", http.StatusSeeOther)
		return
	}
	writeJSON(w, http.StatusOK, schedule)
}

func (s *HTTPServer) apiRuns(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err == nil {
			limit = parsed
		}
	}
	runs, err := s.engine.Runs(limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *HTTPServer) apiClearRuns(w http.ResponseWriter, r *http.Request) {
	if err := s.engine.ClearRuns(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if wantsHTML(r) {
		http.Redirect(w, r, "/?notice=run+log+cleared", http.StatusSeeOther)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
}

func (s *HTTPServer) apiFaktoryQueues(w http.ResponseWriter, r *http.Request) {
	instanceName := r.URL.Query().Get("instance")
	if instanceName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "instance parameter is required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	queues, err := s.engine.FaktoryQueues(ctx, instanceName)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, queues)
}

func (s *HTTPServer) apiScheduleAction(w http.ResponseWriter, r *http.Request) {
	name, action, ok := scheduleActionFromPath(r.URL.Path)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	switch action {
	case "delete":
		if err := s.engine.DeleteSchedule(name); err != nil {
			s.respondScheduleError(w, r, err)
			return
		}
		if wantsHTML(r) {
			http.Redirect(w, r, "/?notice=job+deleted", http.StatusSeeOther)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	case "pause":
		paused, err := s.engine.TogglePause(name)
		if err != nil {
			s.respondScheduleError(w, r, err)
			return
		}
		status := "resumed"
		if paused {
			status = "paused"
		}
		if wantsHTML(r) {
			http.Redirect(w, r, "/?notice=job+"+status, http.StatusSeeOther)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": status, "paused": strconv.FormatBool(paused)})
	case "run":
		run, err := s.engine.TriggerManual(r.Context(), name)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if wantsHTML(r) {
			http.Redirect(w, r, "/?notice=job+triggered", http.StatusSeeOther)
			return
		}
		writeJSON(w, http.StatusAccepted, run)
	}
}

func scheduleActionFromPath(path string) (string, string, bool) {
	const prefix = "/v1/schedules/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	rest := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	switch {
	case strings.HasSuffix(rest, "/run"):
		name := strings.TrimSuffix(rest, "/run")
		name, err := url.PathUnescape(name)
		return name, "run", name != "" && err == nil
	case strings.HasSuffix(rest, "/delete"):
		name := strings.TrimSuffix(rest, "/delete")
		name, err := url.PathUnescape(name)
		return name, "delete", name != "" && err == nil
	case strings.HasSuffix(rest, "/pause"):
		name := strings.TrimSuffix(rest, "/pause")
		name, err := url.PathUnescape(name)
		return name, "pause", name != "" && err == nil
	default:
		return "", "", false
	}
}

func editScheduleNameFromPath(path string) (string, bool) {
	const prefix = "/schedules/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, "/edit") {
		return "", false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/edit")
	name = strings.Trim(name, "/")
	if name == "" {
		return "", false
	}
	decoded, err := url.PathUnescape(name)
	return decoded, err == nil
}

func scheduleFromRequest(r *http.Request) (config.ScheduleConfig, string, error) {
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		var payload struct {
			OriginalName string                `json:"original_name"`
			Schedule     config.ScheduleConfig `json:"schedule"`
		}
		r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return config.ScheduleConfig{}, "", err
		}
		return payload.Schedule, payload.OriginalName, nil
	}

	if err := r.ParseForm(); err != nil {
		return config.ScheduleConfig{}, "", err
	}
	timeout := config.Duration{}
	if raw := strings.TrimSpace(r.FormValue("timeout")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return config.ScheduleConfig{}, "", err
		}
		timeout.Duration = parsed
	}

	targetType := strings.TrimSpace(r.FormValue("target_type"))
	target := config.TargetConfig{Type: targetType}
	switch targetType {
	case "http":
		target.URL = strings.TrimSpace(r.FormValue("target_url"))
		target.Method = strings.TrimSpace(r.FormValue("target_method"))
		target.Headers = parseKeyValues(r.FormValue("target_headers"))
	case "faktory":
		args, err := parseJSONArgs(r.FormValue("target_faktory_args"))
		if err != nil {
			return config.ScheduleConfig{}, "", err
		}
		target.Instance = strings.TrimSpace(r.FormValue("target_faktory_instance"))
		target.Queue = strings.TrimSpace(r.FormValue("target_faktory_queue"))
		target.JobType = strings.TrimSpace(r.FormValue("target_faktory_job_type"))
		target.Args = args
	}
	rateInterval := 0
	if raw := strings.TrimSpace(r.FormValue("rate_interval")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return config.ScheduleConfig{}, "", err
		}
		rateInterval = parsed
	}
	return config.ScheduleConfig{
		Name:            strings.TrimSpace(r.FormValue("name")),
		Description:     strings.TrimSpace(r.FormValue("description")),
		ScheduleType:    strings.TrimSpace(r.FormValue("schedule_type")),
		Cron:            strings.TrimSpace(r.FormValue("cron")),
		Timezone:        strings.TrimSpace(r.FormValue("timezone")),
		RunAt:           strings.TrimSpace(r.FormValue("run_at")),
		StartsAt:        strings.TrimSpace(r.FormValue("starts_at")),
		RateInterval:    rateInterval,
		RateUnit:        strings.TrimSpace(r.FormValue("rate_unit")),
		Timeout:         timeout,
		AllowConcurrent: allowConcurrentFromForm(r),
		Target:          target,
	}, strings.TrimSpace(r.FormValue("original_name")), nil
}

func allowConcurrentFromForm(r *http.Request) bool {
	raw := strings.TrimSpace(r.FormValue("allow_concurrent_runs"))
	return raw == "true" || raw == "on" || raw == "1" || raw == "yes"
}

func parseJSONArgs(raw string) ([]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var args []any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&args); err != nil {
		return nil, config.ConfigError("faktory args must be a JSON array")
	}
	if strings.TrimSpace(raw[decoder.InputOffset():]) != "" {
		return nil, config.ConfigError("faktory args must contain one JSON array")
	}
	return args, nil
}

func parseKeyValues(raw string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			key, value, ok = strings.Cut(line, ":")
		}
		if ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if len(values) == 0 {
		return nil
	}
	return values
}

func (s *HTTPServer) respondScheduleError(w http.ResponseWriter, r *http.Request, err error) {
	if wantsHTML(r) {
		redirectTo := r.Header.Get("Referer")
		if redirectTo == "" {
			redirectTo = "/"
		}
		separator := "?"
		if strings.Contains(redirectTo, "?") {
			separator = "&"
		}
		http.Redirect(w, r, redirectTo+separator+"error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
}

func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func keyValues(values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	lines := make([]string, 0, len(values))
	for _, key := range keys {
		lines = append(lines, key+"="+values[key])
	}
	return strings.Join(lines, "\n")
}

func rawOutput(output map[string]any) string {
	raw, ok := output["raw"]
	if !ok {
		return ""
	}
	value, ok := raw.(string)
	if !ok {
		return ""
	}
	return value
}

func jsonArgs(values []any) string {
	if len(values) == 0 {
		return "[]"
	}
	raw, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func targetJSONArgs(target config.TargetConfig) string {
	if target.Type != "faktory" {
		return "[]"
	}
	return jsonArgs(target.Args)
}

func cronTimeSpan(hourStr, minuteStr, timezone string) template.HTML {
	hour, err1 := strconv.Atoi(hourStr)
	minute, err2 := strconv.Atoi(minuteStr)
	if err1 != nil || err2 != nil {
		return template.HTML(template.HTMLEscapeString(hourStr + ":" + minuteStr))
	}
	return template.HTML(`<span class="wall-time" data-hour="` + template.HTMLEscapeString(strconv.Itoa(hour)) +
		`" data-minute="` + template.HTMLEscapeString(strconv.Itoa(minute)) +
		`" data-source-tz="` + template.HTMLEscapeString(timezone) + `"></span>`)
}

func dateTimeSpan(datetimeStr, timezone string) template.HTML {
	t, err := config.ParseLocalDateTime(datetimeStr, timezone)
	if err != nil {
		return template.HTML(template.HTMLEscapeString(datetimeStr))
	}
	utc := t.UTC().Format(time.RFC3339)
	return template.HTML(`<span class="local-time" data-time="` + template.HTMLEscapeString(utc) + `" data-source-tz="` + template.HTMLEscapeString(timezone) + `"></span>`)
}

func scheduleTimezoneContext(timezone string) template.HTML {
	if timezone == "" {
		timezone = "UTC"
	}
	escaped := template.HTMLEscapeString(timezone)
	return template.HTML(`<span class="timezone-context" data-source-tz="` + escaped + `" hidden></span>`)
}

func scheduleSummary(schedule config.ScheduleConfig) template.HTML {
	switch schedule.ScheduleType {
	case "once":
		if schedule.CompletedAt != nil {
			return "Completed one-time schedule"
		}
		return "Once at " + dateTimeSpan(schedule.RunAt, schedule.Timezone)
	case "rate":
		unit := schedule.RateUnit
		if schedule.RateInterval == 1 {
			unit = strings.TrimSuffix(unit, "s")
		}
		return template.HTML(fmt.Sprintf("Every %d %s", schedule.RateInterval, template.HTMLEscapeString(unit)))
	}
	fields := strings.Fields(schedule.Cron)
	if len(fields) != 5 {
		return template.HTML(template.HTMLEscapeString(schedule.Cron))
	}

	tz := schedule.Timezone
	minute, hour, dom, month, dow := fields[0], fields[1], fields[2], fields[3], fields[4]
	switch {
	case strings.HasPrefix(minute, "*/") && hour == "*" && dom == "*" && month == "*" && dow == "*":
		interval := strings.TrimPrefix(minute, "*/")
		if interval == "1" {
			return "Every minute"
		}
		return template.HTML("Every " + template.HTMLEscapeString(interval) + " minutes")
	case isCronNumber(minute) && hour == "*" && dom == "*" && month == "*" && dow == "*":
		return template.HTML("Hourly at " + minuteLabel(minute))
	case isCronNumber(minute) && isCronNumber(hour) && dom == "*" && month == "*" && dow == "*":
		return "Daily at " + cronTimeSpan(hour, minute, tz)
	case isCronNumber(minute) && isCronNumber(hour) && dom == "*" && month == "*" && isCronNumber(dow):
		return template.HTML("Weekly on "+weekdayLabel(dow)+" at ") + cronTimeSpan(hour, minute, tz)
	case isCronNumber(minute) && isCronNumber(hour) && isCronNumber(dom) && month == "*" && dow == "*":
		return template.HTML("Monthly on the "+ordinal(dom)+" at ") + cronTimeSpan(hour, minute, tz)
	case isCronNumber(minute) && isCronNumber(hour) && isCronNumber(dom) && isCronNumber(month) && dow == "*":
		return template.HTML("Every "+monthLabel(month)+" "+dayNumber(dom)+" at ") + cronTimeSpan(hour, minute, tz)
	default:
		return template.HTML("Cron: " + template.HTMLEscapeString(schedule.Cron))
	}
}

func scheduleDetails(schedule config.ScheduleConfig) template.HTML {
	var parts []template.HTML
	switch schedule.ScheduleType {
	case "once":
		// no extra detail needed; summary has the datetime
	case "rate":
		if schedule.StartsAt != "" {
			parts = append(parts, "Starts "+dateTimeSpan(schedule.StartsAt, schedule.Timezone))
		}
	default:
		if schedule.Cron != "" {
			parts = append(parts, template.HTML(template.HTMLEscapeString(schedule.Cron)))
		}
	}
	var result template.HTML
	for i, p := range parts {
		if i > 0 {
			result += " · "
		}
		result += p
	}
	return result + scheduleTimezoneContext(schedule.Timezone)
}

func concurrencyLabel(schedule config.ScheduleConfig) string {
	if schedule.AllowsConcurrentRuns() {
		return "Can overlap"
	}
	return "No overlap"
}

func nextRunLabel(schedule config.ScheduleConfig) template.HTML {
	if schedule.Paused {
		return `<span class="status status-paused">paused</span>`
	}
	if !schedule.NextRun.IsZero() {
		return template.HTML(`<span class="local-time" data-time="` +
			template.HTMLEscapeString(schedule.NextRun.UTC().Format(time.RFC3339)) +
			`" data-source-tz="` + template.HTMLEscapeString(schedule.Timezone) + `"></span>`)
	}
	if schedule.ScheduleType == "once" && schedule.CompletedAt != nil {
		return `<span class="status status-succeeded">completed</span>`
	}
	return `<span class="muted">no upcoming run</span>`
}

func displayRunTime(run runView) template.HTML {
	if run.StartedAt.IsZero() {
		return ""
	}
	return template.HTML(`<span class="local-time" data-time="` +
		template.HTMLEscapeString(run.StartedAt.UTC().Format(time.RFC3339)) +
		`" data-source-tz="` + template.HTMLEscapeString(run.DisplayTimezone) + `"></span>`)
}

func displayTime(value time.Time, timezone string) string {
	if value.IsZero() {
		return ""
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		location = time.UTC
		timezone = "UTC"
	}
	return value.In(location).Format("Jan 2, 2006 3:04 PM") + " " + timezone
}

func localDateTimeString(t time.Time) string {
	return t.Format("2006-01-02T15:04")
}

func isCronNumber(value string) bool {
	_, err := strconv.Atoi(value)
	return err == nil
}

func minuteLabel(minute string) string {
	value, err := strconv.Atoi(minute)
	if err != nil {
		return ":" + minute
	}
	return fmt.Sprintf(":%02d", value)
}

func timeLabel(hour string, minute string) string {
	hourValue, hourErr := strconv.Atoi(hour)
	minuteValue, minuteErr := strconv.Atoi(minute)
	if hourErr != nil || minuteErr != nil {
		return hour + ":" + minute
	}
	period := "AM"
	displayHour := hourValue
	if displayHour >= 12 {
		period = "PM"
	}
	displayHour = displayHour % 12
	if displayHour == 0 {
		displayHour = 12
	}
	return fmt.Sprintf("%d:%02d %s", displayHour, minuteValue, period)
}

func weekdayLabel(day string) string {
	switch day {
	case "0", "7":
		return "Sunday"
	case "1":
		return "Monday"
	case "2":
		return "Tuesday"
	case "3":
		return "Wednesday"
	case "4":
		return "Thursday"
	case "5":
		return "Friday"
	case "6":
		return "Saturday"
	default:
		return "day " + day
	}
}

func monthLabel(month string) string {
	switch month {
	case "1":
		return "Jan"
	case "2":
		return "Feb"
	case "3":
		return "Mar"
	case "4":
		return "Apr"
	case "5":
		return "May"
	case "6":
		return "Jun"
	case "7":
		return "Jul"
	case "8":
		return "Aug"
	case "9":
		return "Sep"
	case "10":
		return "Oct"
	case "11":
		return "Nov"
	case "12":
		return "Dec"
	default:
		return "month " + month
	}
}

func ordinal(day string) string {
	return dayNumber(day) + ordinalSuffix(day)
}

func dayNumber(day string) string {
	value, err := strconv.Atoi(day)
	if err != nil {
		return day
	}
	return strconv.Itoa(value)
}

func ordinalSuffix(day string) string {
	value, err := strconv.Atoi(day)
	if err != nil {
		return ""
	}
	if value%100 >= 11 && value%100 <= 13 {
		return "th"
	}
	switch value % 10 {
	case 1:
		return "st"
	case 2:
		return "nd"
	case 3:
		return "rd"
	default:
		return "th"
	}
}

var timezoneOptions = []string{
	"UTC",
	"America/New_York",
	"America/Chicago",
	"America/Denver",
	"America/Los_Angeles",
	"America/Phoenix",
	"America/Anchorage",
	"Pacific/Honolulu",
	"America/Toronto",
	"America/Vancouver",
	"America/Mexico_City",
	"America/Sao_Paulo",
	"Europe/London",
	"Europe/Dublin",
	"Europe/Paris",
	"Europe/Berlin",
	"Europe/Amsterdam",
	"Europe/Madrid",
	"Europe/Rome",
	"Europe/Stockholm",
	"Europe/Warsaw",
	"Europe/Istanbul",
	"Africa/Johannesburg",
	"Asia/Dubai",
	"Asia/Jerusalem",
	"Asia/Kolkata",
	"Asia/Bangkok",
	"Asia/Singapore",
	"Asia/Hong_Kong",
	"Asia/Shanghai",
	"Asia/Tokyo",
	"Asia/Seoul",
	"Australia/Perth",
	"Australia/Sydney",
	"Pacific/Auckland",
}

func timezonesWithCurrent(current string) []string {
	options := append([]string(nil), timezoneOptions...)
	for _, option := range options {
		if option == current {
			return options
		}
	}
	if current == "" {
		return options
	}
	return append([]string{current}, options...)
}

const uiHTML = `{{define "index"}}<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Clock Relay</title>
  <style>{{template "styles"}}</style>
</head>
<body>
  <header>
    <div class="header-inner">
      <div>
        <h1>Clock Relay</h1>
        <div class="summary">{{len .Schedules}} jobs · {{len .Runs}} recent runs</div>
      </div>
      <nav>
        <label class="toolbar-field">My timezone
          <select id="work-tz-select" class="compact" title="Timezone used to show times and default new jobs">
            {{range .Timezones}}<option value="{{.}}">{{.}}</option>
            {{end}}
          </select>
        </label>
        <label class="toolbar-field">Time format
          <select id="time-format-select" class="compact" title="Time display format">
            <option value="auto">Auto</option>
            <option value="12h">12-hour</option>
            <option value="24h">24-hour</option>
          </select>
        </label>
      </nav>
    </div>
  </header>
  <main>
    {{if .Error}}<div class="alert error">{{.Error}}</div>{{end}}
    {{if .Notice}}<div class="alert notice">{{.Notice}}</div>{{end}}

	    <section class="table-section jobs-section" data-live-section="jobs">
      <div class="section-head">
        <h2>Jobs</h2>
        <a class="button primary" href="/schedules/new">New job</a>
      </div>
      {{if .Schedules}}
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Schedule</th>
            <th>Target</th>
            <th>Next Run</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
        {{range .Schedules}}
          <tr{{if .Paused}} class="row-paused"{{end}}>
            <td data-label="Name">
              <strong>{{.Name}}</strong>
              {{if .Description}}<div class="muted">{{.Description}}</div>{{end}}
            </td>
            <td data-label="Schedule"><strong>{{scheduleSummary .}}</strong><div class="muted">{{scheduleDetails .}}</div></td>
            <td data-label="Target"><code>{{.Target.Type}}</code>{{if .Target.Instance}}<div class="muted output">{{.Target.Instance}}</div>{{else if .Target.URL}}<div class="muted output">{{.Target.URL}}</div>{{end}}</td>
            <td data-label="Next Run">{{nextRunLabel .}}</td>
            <td data-label="Actions" class="actions">
              <form class="inline" action="/v1/schedules/{{pathEscape .Name}}/pause" method="post">
                <button class="secondary" type="submit">{{if .Paused}}Resume{{else}}Pause{{end}}</button>
              </form>
              {{if not .Paused}}
              <form class="inline" action="/v1/schedules/{{pathEscape .Name}}/run" method="post">
                <button type="submit">Run</button>
              </form>
              {{end}}
              <div class="overflow-menu">
                <button class="button secondary overflow-toggle" type="button">&#xb7;&#xb7;&#xb7;</button>
                <div class="overflow-items">
                  {{if uiEditableTarget .Target.Type}}
                  <a href="/schedules/{{pathEscape .Name}}/edit">Edit</a>
                  {{end}}
                  <form action="/v1/schedules/{{pathEscape .Name}}/delete" method="post">
                    <button class="overflow-danger" type="submit">Delete</button>
                  </form>
                </div>
              </div>
            </td>
          </tr>
        {{end}}
        </tbody>
      </table>
      {{else}}
      <div class="empty">No jobs are configured.</div>
      {{end}}
    </section>

	    <section class="table-section runs-section" data-live-section="runs" data-job-filter="{{.JobFilter}}">
      <div class="section-head">
        <h2>Run Log</h2>
        <div class="section-actions">
          <select id="run-job-filter" class="compact" title="Filter runs by job">
            <option value="">All jobs</option>
            {{range .Schedules}}<option value="{{.Name}}"{{if eq $.JobFilter .Name}} selected{{end}}>{{.Name}}</option>
            {{end}}
          </select>
          <form class="inline" action="/v1/runs/clear" method="post">
            <button class="danger" type="submit">Clear run log</button>
          </form>
        </div>
      </div>
      {{if .Runs}}
      <table>
        <thead>
          <tr>
            <th>Status</th>
            <th>Job</th>
            <th>Trigger</th>
            <th>Started</th>
            <th>Output</th>
          </tr>
        </thead>
        <tbody>
        {{range .Runs}}
          <tr>
            <td data-label="Status"><span class="status status-{{.Status}}">{{.Status}}</span></td>
            <td data-label="Job"><strong>{{.ScheduleName}}</strong><div class="muted">{{.ID}}</div></td>
            <td data-label="Trigger">{{.TriggeredBy}}<div class="muted">{{.TargetType}}</div></td>
            <td data-label="Started">{{displayRunTime .}}</td>
            <td data-label="Output">
              {{if .Error}}
              <button class="button secondary compact" type="button" data-run-output="{{.Error}}" data-run-title="{{.ScheduleName}} · {{.ID}}">View error</button>
              {{else if rawOutput .StructuredOutput}}
              <button class="button secondary compact" type="button" data-run-output="{{rawOutput .StructuredOutput}}" data-run-title="{{.ScheduleName}} · {{.ID}}">View output</button>
              {{else}}
              <span class="muted">none</span>
              {{end}}
            </td>
          </tr>
        {{end}}
        </tbody>
      </table>
      {{else}}
      <div class="empty">No runs yet.</div>
      {{end}}
    </section>
  </main>
  <dialog class="output-dialog" id="run-output-dialog">
    <div class="dialog-head">
      <div>
        <h2>Run Output</h2>
        <div class="muted" id="run-output-title"></div>
      </div>
      <form method="dialog">
        <button class="button secondary compact" type="submit">Close</button>
      </form>
    </div>
    <pre id="run-output-body"></pre>
  </dialog>
  {{template "time_helpers"}}
	  <script>
	    const liveSections = ["jobs", "runs"];
	    const outputDialog = document.getElementById("run-output-dialog");
	    const outputTitle = document.getElementById("run-output-title");
	    const outputBody = document.getElementById("run-output-body");

	    ClockRelayTime.bindToolbar(document);
	    ClockRelayTime.formatPage(document);

	    document.addEventListener("change", (event) => {
	      if (event.target.id === "run-job-filter") {
	        const url = new URL(window.location);
	        const value = event.target.value;
	        if (value) {
	          url.searchParams.set("job", value);
	        } else {
	          url.searchParams.delete("job");
	        }
	        window.location = url;
	      }
	    });

	    document.addEventListener("click", (event) => {
	      const toggle = event.target.closest(".overflow-toggle");
	      if (toggle) {
	        const menu = toggle.closest(".overflow-menu");
	        const wasOpen = menu.classList.contains("open");
	        document.querySelectorAll(".overflow-menu.open").forEach(m => m.classList.remove("open"));
	        if (!wasOpen) menu.classList.add("open");
	        return;
	      }
	      if (!event.target.closest(".overflow-items")) {
	        document.querySelectorAll(".overflow-menu.open").forEach(m => m.classList.remove("open"));
	      }

	      const button = event.target.closest("[data-run-output]");
	      if (!button) {
	        if (event.target === outputDialog) {
	          outputDialog.close();
	        }
	        return;
	      }
	      outputTitle.textContent = button.dataset.runTitle || "";
	      outputBody.textContent = button.dataset.runOutput || "";
	      if (typeof outputDialog.showModal === "function") {
	        outputDialog.showModal();
	      } else {
	        outputDialog.setAttribute("open", "");
	      }
	    });

	    const refreshLiveSections = async () => {
	      if (document.hidden) {
	        return;
	      }
	      try {
	        const pollURL = new URL("/", window.location.origin);
	        const jobFilter = new URL(window.location).searchParams.get("job");
	        if (jobFilter) pollURL.searchParams.set("job", jobFilter);
	        const response = await fetch(pollURL, {
	          headers: { "Accept": "text/html" },
	          cache: "no-store",
	        });
	        if (!response.ok) {
	          return;
	        }
	        const html = await response.text();
	        const nextDocument = new DOMParser().parseFromString(html, "text/html");
	        liveSections.forEach((name) => {
	          const current = document.querySelector("[data-live-section='" + name + "']");
	          const next = nextDocument.querySelector("[data-live-section='" + name + "']");
	          if (current && next) {
	            current.replaceWith(next);
	          }
	        });
	        ClockRelayTime.formatPage(document);
	      } catch (_) {
	      }
	    };
	    window.setInterval(refreshLiveSections, 3000);
	  </script>
	</body>
	</html>{{end}}

{{define "time_helpers"}}
  <script>
    window.ClockRelayTime = (() => {
      const timezoneKey = "clock-relay-work-timezone";
      const formatKey = "clock-relay-time-format";
      const legacyTimezoneKey = "rg-display-tz";
      const legacyFormatKey = "rg-time-format";

      const browserTimezone = () => {
        try {
          return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
        } catch (_) {
          return "UTC";
        }
      };

      const stored = (key) => {
        try {
          return localStorage.getItem(key);
        } catch (_) {
          return null;
        }
      };

      const store = (key, value) => {
        try {
          localStorage.setItem(key, value);
        } catch (_) {
        }
      };

      const workTimezone = () => stored(timezoneKey) || stored(legacyTimezoneKey) || browserTimezone();
      const setWorkTimezone = (tz) => store(timezoneKey, tz || "UTC");
      const timeFormat = () => stored(formatKey) || stored(legacyFormatKey) || "auto";
      const setTimeFormat = (fmt) => store(formatKey, fmt || "auto");

      const ensureOption = (select, value) => {
        if (!select || !value) return;
        if (Array.from(select.options).some((option) => option.value === value)) return;
        const option = document.createElement("option");
        option.value = value;
        option.textContent = value;
        select.prepend(option);
      };

      const hour12Option = () => {
        const fmt = timeFormat();
        if (fmt === "12h") return true;
        if (fmt === "24h") return false;
        return undefined;
      };

      const dateTimeOptions = (timeOnly) => {
        const opts = timeOnly
          ? { hour: "numeric", minute: "2-digit" }
          : { year: "numeric", month: "short", day: "numeric", hour: "numeric", minute: "2-digit" };
        opts.timeZone = workTimezone();
        const hour12 = hour12Option();
        if (hour12 !== undefined) opts.hour12 = hour12;
        return opts;
      };

      const shortTimezoneName = (date) => {
        try {
          const parts = new Intl.DateTimeFormat(undefined, { timeZone: workTimezone(), timeZoneName: "short" }).formatToParts(date);
          const part = parts.find((p) => p.type === "timeZoneName");
          return part ? part.value : workTimezone();
        } catch (_) {
          return workTimezone();
        }
      };

      const formatPage = (root) => {
        const tz = workTimezone();
        root.querySelectorAll(".local-time[data-time]").forEach((el) => {
          try {
            const date = new Date(el.dataset.time);
            const timeOnly = el.dataset.format === "time";
            el.textContent = date.toLocaleString(undefined, dateTimeOptions(timeOnly));
            if (!timeOnly) el.textContent += " " + shortTimezoneName(date);
          } catch (_) {
            el.textContent = el.dataset.time || "";
          }
        });

        root.querySelectorAll(".wall-time[data-hour][data-minute]").forEach((el) => {
          try {
            const hour = Number(el.dataset.hour);
            const minute = Number(el.dataset.minute);
            const date = new Date(2000, 0, 1, hour, minute, 0, 0);
            const opts = { hour: "numeric", minute: "2-digit" };
            const hour12 = hour12Option();
            if (hour12 !== undefined) opts.hour12 = hour12;
            el.textContent = date.toLocaleTimeString(undefined, opts);
          } catch (_) {
            el.textContent = String(el.dataset.hour || "") + ":" + String(el.dataset.minute || "").padStart(2, "0");
          }
        });

        root.querySelectorAll(".timezone-context[data-source-tz]").forEach((el) => {
          const source = el.dataset.sourceTz || "UTC";
          const same = source === tz;
          el.hidden = same;
          el.textContent = same ? "" : " · Configured in " + source;
        });
      };

      const bindToolbar = (root) => {
        const timezoneSelect = root.getElementById?.("work-tz-select");
        if (timezoneSelect) {
          selectTimezone(timezoneSelect, workTimezone());
          timezoneSelect.addEventListener("change", (event) => {
            setWorkTimezone(event.target.value);
            formatPage(document);
          });
        }

        const formatSelect = root.getElementById?.("time-format-select");
        if (formatSelect) {
          formatSelect.value = timeFormat();
          formatSelect.addEventListener("change", (event) => {
            setTimeFormat(event.target.value);
            formatPage(document);
          });
        }
      };

      const localDateTime = (date, timezone) => {
        const tz = timezone || workTimezone();
        try {
          const parts = new Intl.DateTimeFormat("en-CA", {
            timeZone: tz,
            year: "numeric",
            month: "2-digit",
            day: "2-digit",
            hour: "2-digit",
            minute: "2-digit",
            hourCycle: "h23",
          }).formatToParts(date);
          const part = (type) => parts.find((p) => p.type === type)?.value;
          const year = part("year");
          const month = part("month");
          const day = part("day");
          const hour = part("hour");
          const minute = part("minute");
          if (year && month && day && hour && minute) {
            return year + "-" + month + "-" + day + "T" + hour + ":" + minute;
          }
        } catch (_) {
        }
        const pad = (value) => String(value).padStart(2, "0");
        return [
          date.getFullYear(),
          pad(date.getMonth() + 1),
          pad(date.getDate()),
        ].join("-") + "T" + [pad(date.getHours()), pad(date.getMinutes())].join(":");
      };

      const preferredScheduleTimezone = () => workTimezone();

      const selectTimezone = (select, timezone) => {
        if (!select || !timezone) return;
        ensureOption(select, timezone);
        select.value = timezone;
      };

      return {
        bindToolbar,
        formatPage,
        localDateTime,
        preferredScheduleTimezone,
        selectTimezone,
        setWorkTimezone,
        workTimezone,
      };
    })();
  </script>
{{end}}

{{define "schedule_form"}}<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Title}} · Clock Relay</title>
  <style>{{template "styles"}}</style>
</head>
<body>
  <header>
    <div class="header-inner">
      <div>
        <h1>{{.Title}}</h1>
        <div class="summary">Define when a job should trigger and what target Clock Relay should call.</div>
      </div>
      <nav>
        <a class="button secondary" href="/">Back</a>
      </nav>
    </div>
  </header>
  <main>
    {{if .Error}}<div class="alert error">{{.Error}}</div>{{end}}
	<form class="job-form" action="/v1/schedules" method="post" data-job-form data-new-job="{{.IsNew}}">
      <input type="hidden" name="original_name" value="{{.OriginalName}}">
      <section class="form-card">
        <div class="form-card-head">
          <h2>Job Details</h2>
        </div>
        <div class="form-grid">
          <label>Name
            <input name="name" required value="{{.Schedule.Name}}" placeholder="nightly-sync">
          </label>
          <label>Timeout
            <input name="timeout" value="{{.Schedule.Timeout}}">
          </label>
          <label class="span-2">Description
            <input name="description" value="{{.Schedule.Description}}">
          </label>
          <label>Allow concurrent runs
            <select name="allow_concurrent_runs">
              <option value="false" {{if not (allowConcurrent .Schedule)}}selected{{end}}>No</option>
              <option value="true" {{if allowConcurrent .Schedule}}selected{{end}}>Yes</option>
            </select>
          </label>
        </div>
      </section>

      <section class="form-card">
        <div class="form-card-head">
          <h2>Schedule</h2>
          <p>Choose a simple interval, a one-time run, or cron for calendar-based schedules.</p>
        </div>
        <div class="form-grid">
          <fieldset class="type-picker schedule-picker span-4">
            <label class="type-option">
              <input type="radio" name="schedule_type" value="rate" {{if eq .Schedule.ScheduleType "rate"}}checked{{end}}>
              <span>
                <strong>Rate</strong>
                <small>Run every N minutes, hours, or days from a start time.</small>
              </span>
            </label>
            <label class="type-option">
              <input type="radio" name="schedule_type" value="once" {{if eq .Schedule.ScheduleType "once"}}checked{{end}}>
              <span>
                <strong>Once</strong>
                <small>Run one time at a specific local date and time.</small>
              </span>
            </label>
            <label class="type-option">
              <input type="radio" name="schedule_type" value="cron" {{if or (eq .Schedule.ScheduleType "") (eq .Schedule.ScheduleType "cron")}}checked{{end}}>
              <span>
                <strong>Cron</strong>
                <small>Use a five-field expression for calendar-based schedules.</small>
              </span>
            </label>
          </fieldset>

          <label class="span-2">Schedule Timezone
            <select name="timezone" data-timezone-select>
              {{range .Timezones}}<option value="{{.}}" {{if eq . $.Schedule.Timezone}}selected{{end}}>{{.}}</option>{{end}}
            </select>
            <span class="field-hint">New jobs default to your saved timezone. This schedule runs in <strong data-timezone-summary>{{.Schedule.Timezone}}</strong>.</span>
          </label>

          <div class="mode-panel span-4" data-schedule-panel="rate">
            <div class="panel-copy">
              <h2>Rate Schedule</h2>
              <p>Days are fixed 24-hour intervals. Use cron for same-local-time daily schedules.</p>
            </div>
            <div class="nested-grid">
              <label>Every
                <input name="rate_interval" type="number" min="1" value="{{.Schedule.RateInterval}}" data-required-for-schedule="rate">
              </label>
              <label>Unit
                <select name="rate_unit" data-required-for-schedule="rate">
                  <option value="minutes" {{if eq .Schedule.RateUnit "minutes"}}selected{{end}}>minutes</option>
                  <option value="hours" {{if eq .Schedule.RateUnit "hours"}}selected{{end}}>hours</option>
                  <option value="days" {{if eq .Schedule.RateUnit "days"}}selected{{end}}>days</option>
                </select>
              </label>
              <label class="span-2">Starts At
                <input name="starts_at" type="datetime-local" value="{{.Schedule.StartsAt}}" data-required-for-schedule="rate">
                <span class="field-hint">Interpreted in <strong data-timezone-summary>{{.Schedule.Timezone}}</strong>.</span>
              </label>
            </div>
          </div>

          <div class="mode-panel span-4" data-schedule-panel="once">
            <div class="panel-copy">
              <h2>One-Time Schedule</h2>
              <p>After the run is created, the job stays visible as completed.</p>
            </div>
            <div class="nested-grid">
              <label class="span-2">Run At
                <input name="run_at" type="datetime-local" value="{{.Schedule.RunAt}}" data-required-for-schedule="once">
                <span class="field-hint">Interpreted in <strong data-timezone-summary>{{.Schedule.Timezone}}</strong>.</span>
              </label>
            </div>
          </div>

          <div class="mode-panel span-4" data-schedule-panel="cron">
            <div class="panel-copy">
              <h2>Cron Schedule</h2>
              <p>Use standard five-field cron: minute, hour, day of month, month, day of week.</p>
            </div>
            <label>Cron Expression
              <input name="cron" value="{{.Schedule.Cron}}" data-required-for-schedule="cron" placeholder="*/5 * * * *">
              <span class="field-hint">Evaluated in <strong data-timezone-summary>{{.Schedule.Timezone}}</strong>.</span>
            </label>
          </div>
        </div>
      </section>

      <section class="form-card">
        <div class="form-card-head">
          <h2>Job Type</h2>
          <p>Choose how Clock Relay should trigger this job.</p>
        </div>
        <div class="form-grid">
          <fieldset class="type-picker span-4">
            <label class="type-option">
              <input type="radio" name="target_type" value="http" {{if or (eq .Schedule.Target.Type "") (eq .Schedule.Target.Type "http")}}checked{{end}}>
              <span>
                <strong>Web request</strong>
                <small>Send an HTTP request to an app or worker endpoint. Good for triggering work owned by another service.</small>
              </span>
            </label>
            <label class="type-option">
              <input type="radio" name="target_type" value="faktory" {{if eq .Schedule.Target.Type "faktory"}}checked{{end}}>
              <span>
                <strong>Faktory job</strong>
                <small>Enqueue a native Faktory job. Clock Relay records success when Faktory accepts the job.</small>
              </span>
            </label>
          </fieldset>
        </div>
      </section>

      <section class="form-card" data-target-panel="http">
        <div class="form-card-head">
          <h2>Web Request Parameters</h2>
          <p>Clock Relay sends JSON by default with the run ID, job name, scheduled time, and trigger source. Add headers for auth or internal routing.</p>
        </div>
        <div class="form-grid">
          <div class="span-4">
            <div class="nested-grid">
              <label class="span-3">HTTP URL
                <input name="target_url" value="{{.Schedule.Target.URL}}" data-required-for="http" placeholder="http://localhost:3000/internal/jobs/nightly-sync">
              </label>
              <label>Method
                <select name="target_method">
                  <option value="POST" {{if eq .Schedule.Target.Method "POST"}}selected{{end}}>POST</option>
                  <option value="PUT" {{if eq .Schedule.Target.Method "PUT"}}selected{{end}}>PUT</option>
                  <option value="PATCH" {{if eq .Schedule.Target.Method "PATCH"}}selected{{end}}>PATCH</option>
                  <option value="GET" {{if eq .Schedule.Target.Method "GET"}}selected{{end}}>GET</option>
                </select>
              </label>
              <label class="span-2">Headers
                <textarea name="target_headers" placeholder="Authorization=Bearer token">{{keyValues .Schedule.Target.Headers}}</textarea>
              </label>
            </div>
          </div>
        </div>
      </section>

      <section class="form-card" data-target-panel="faktory">
        <div class="form-card-head">
          <h2>Faktory Job Parameters</h2>
          <p>Clock Relay enqueues the job and records the Faktory JID. The worker receives only the configured args.</p>
        </div>
        <div class="form-grid">
          <div class="span-4">
            <div class="nested-grid">
              {{if .FaktoryInstances}}
              <label class="span-2">Instance
                <select name="target_faktory_instance" data-required-for="faktory">
                  {{range .FaktoryInstances}}<option value="{{.}}" {{if eq . $.Schedule.Target.Instance}}selected{{end}}>{{.}}</option>{{end}}
                </select>
              </label>
              {{else}}
              <div class="span-4">
                <div class="alert error">No Faktory instances configured. Add instances to the <code>faktory</code> section of your config file.</div>
              </div>
              {{end}}
              <label>Queue
                <input name="target_faktory_queue" value="{{.Schedule.Target.Queue}}" placeholder="default">
              </label>
              <label>Job Type
                <input name="target_faktory_job_type" value="{{.Schedule.Target.JobType}}" data-required-for="faktory" placeholder="nightly_sync_job">
              </label>
              <label class="span-4">Args JSON
                <textarea name="target_faktory_args" data-required-for="faktory">{{targetJSONArgs .Schedule.Target}}</textarea>
              </label>
            </div>
          </div>
        </div>
      </section>

      <noscript>
        <section class="form-card">
          <label>Target
            <select name="target_type">
            <option value="http" {{if eq .Schedule.Target.Type "http"}}selected{{end}}>http</option>
            <option value="faktory" {{if eq .Schedule.Target.Type "faktory"}}selected{{end}}>faktory</option>
            </select>
          </label>
        </section>
      </noscript>
      <div class="form-actions">
        <button type="submit">{{.SubmitLabel}}</button>
        <a class="button secondary" href="/">Cancel</a>
      </div>
    </form>
  </main>
  {{template "time_helpers"}}
		<script>
		  const form = document.querySelector("[data-job-form]");
		  if (form) {
			    const hydrateNewScheduleTimes = () => {
		      if (form.dataset.newJob !== "true") {
		        return;
		      }
		      const timezone = form.querySelector("[data-timezone-select]");
		      const preferredTimezone = ClockRelayTime.preferredScheduleTimezone();
		      ClockRelayTime.selectTimezone(timezone, preferredTimezone);
		      const now = new Date();
			      const startsAt = form.querySelector("input[name='starts_at']");
			      if (startsAt) {
			        startsAt.value = ClockRelayTime.localDateTime(now, preferredTimezone);
			      }
			      const runAt = form.querySelector("input[name='run_at']");
			      if (runAt) {
			        runAt.value = ClockRelayTime.localDateTime(new Date(now.getTime() + 5 * 60 * 1000), preferredTimezone);
				      }
			    };

			    const syncTimezoneLabels = () => {
			      const timezone = form.querySelector("[data-timezone-select]")?.value || "UTC";
			      form.querySelectorAll("[data-timezone-summary]").forEach((field) => {
			        field.textContent = timezone;
			      });
			    };

			    const syncSchedule = () => {
        const selected = form.querySelector("input[name='schedule_type']:checked")?.value || "rate";
        form.querySelectorAll("[data-schedule-panel]").forEach((panel) => {
          const active = panel.dataset.schedulePanel === selected;
          panel.hidden = !active;
          panel.querySelectorAll("input, select, textarea").forEach((field) => {
            field.disabled = !active;
          });
        });
	        form.querySelectorAll("[data-required-for-schedule]").forEach((field) => {
	          field.required = field.dataset.requiredForSchedule === selected;
	        });
	        syncTimezoneLabels();
	      };

      const syncType = () => {
        const selected = form.querySelector("input[name='target_type']:checked")?.value || "http";
        form.querySelectorAll("[data-target-panel]").forEach((panel) => {
          const active = panel.dataset.targetPanel === selected;
          panel.hidden = !active;
          panel.querySelectorAll("input, select, textarea").forEach((field) => {
            field.disabled = !active;
          });
        });
        form.querySelectorAll("[data-required-for]").forEach((field) => {
          field.required = field.dataset.requiredFor === selected;
        });
      };
      form.querySelectorAll("input[name='target_type']").forEach((field) => {
        field.addEventListener("change", syncType);
      });
	      form.querySelectorAll("input[name='schedule_type']").forEach((field) => {
	        field.addEventListener("change", syncSchedule);
	      });
	      form.querySelector("[data-timezone-select]")?.addEventListener("change", syncTimezoneLabels);
	      hydrateNewScheduleTimes();
	      syncSchedule();
		      syncType();
		    }
  </script>
</body>
</html>{{end}}

{{define "new_job_picker"}}<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>New Job · Clock Relay</title>
  <style>{{template "styles"}}</style>
</head>
<body>
  <header>
    <div class="header-inner">
      <div>
        <h1>New Job</h1>
        <div class="summary">Choose the type of job to create.</div>
      </div>
      <nav>
        <a class="button secondary" href="/">Back</a>
      </nav>
    </div>
  </header>
  <main>
    <div class="picker-grid">
      {{if .FaktoryAvailable}}
      <a class="picker-card" href="/schedules/new?type=faktory">
        <strong>Faktory job</strong>
        <p>Enqueue a native Faktory job. Clock Relay records success when Faktory accepts the job.</p>
      </a>
      {{else}}
      <div class="picker-card disabled">
        <strong>Faktory job</strong>
        <p>No Faktory instances configured. Add instances to the <code>faktory</code> section of your config file.</p>
      </div>
      {{end}}
      <div class="picker-card disabled">
        <strong>Web request</strong>
        <p>Send an HTTP request to an app or worker endpoint. Good for triggering work owned by another service.</p>
        <span class="picker-badge">Coming soon</span>
      </div>
    </div>
  </main>
</body>
</html>{{end}}

{{define "faktory_form"}}<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Title}} · Clock Relay</title>
  <style>
    {{template "styles"}}

    .wiz { max-width: 620px; margin: 0 auto; }
    .wiz-narrative {
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 10px;
      padding: 20px 24px;
      font-size: 16px;
      line-height: 1.6;
      color: var(--muted);
      border-left: 3px solid var(--accent);
    }
    .wiz-narrative b { color: var(--text); font-weight: 700; }
    .wiz-narrative i { color: var(--muted); font-style: italic; }
    .wiz-narrative .wiz-frag { display: none; }
    .wiz-narrative .wiz-frag.visible { display: inline; }
    .wiz-step {
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 10px;
      padding: 28px 24px;
      display: none;
    }
    .wiz-step.active { display: block; }
    .wiz-step h2 {
      font-size: 18px;
      margin: 0 0 4px;
    }
    .wiz-step .wiz-hint {
      color: var(--muted);
      margin: 0 0 20px;
      font-size: 14px;
    }
    .wiz-fields {
      display: grid;
      grid-template-columns: 1fr 1fr;
      gap: 14px;
    }
    .wiz-fields .full { grid-column: 1 / -1; }
    .wiz-nav {
      display: flex;
      align-items: center;
      justify-content: space-between;
      padding: 4px 0;
    }
    .wiz-nav .wiz-back {
      color: var(--muted);
      text-decoration: none;
      font-size: 14px;
      font-weight: 600;
      cursor: pointer;
      border: 0;
      background: 0;
      padding: 0;
    }
    .wiz-nav .wiz-back:hover { color: var(--text); }
    .wiz-progress {
      display: flex;
      gap: 6px;
      align-items: center;
    }
    .wiz-dot {
      width: 8px;
      height: 8px;
      border-radius: 50%;
      background: var(--line);
      transition: background 0.2s;
    }
    .wiz-dot.done { background: var(--accent); }
    .wiz-dot.current { background: var(--accent); box-shadow: 0 0 0 3px rgba(18,124,117,0.18); }
    .wiz-schedule-cards {
      display: grid;
      grid-template-columns: repeat(3, 1fr);
      gap: 10px;
      margin-bottom: 4px;
    }
    .wiz-scard {
      display: block;
      border: 2px solid var(--line);
      border-radius: 8px;
      padding: 16px;
      cursor: pointer;
      transition: border-color 0.15s;
      text-align: left;
      background: var(--panel);
      font: inherit;
      color: var(--text);
    }
    .wiz-scard:hover { border-color: var(--accent); }
    .wiz-scard.selected { border-color: var(--accent); background: #f0faf9; }
    .wiz-scard strong { display: block; font-size: 15px; margin-bottom: 6px; }
    .wiz-scard small { display: block; color: var(--muted); font-size: 13px; line-height: 1.4; }
    .wiz-review-table {
      width: 100%;
      border-collapse: collapse;
    }
    .wiz-review-table td {
      padding: 10px 0;
      border-bottom: 1px solid var(--line);
      vertical-align: top;
    }
    .wiz-review-table tr:last-child td { border-bottom: 0; }
    .wiz-review-table td:first-child {
      color: var(--muted);
      font-size: 12px;
      font-weight: 700;
      text-transform: uppercase;
      width: 120px;
      padding-right: 16px;
    }
    .wiz-review-table td:last-child { font-weight: 500; }
    .wiz-review-table code {
      background: #eef1f5;
      border-radius: 4px;
      padding: 2px 6px;
      font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
      font-size: 13px;
    }
    @media (max-width: 760px) {
      .wiz-fields { grid-template-columns: 1fr; }
      .wiz-schedule-cards { grid-template-columns: 1fr; }
    }
  </style>
</head>
<body>
  <header>
    <div class="header-inner">
      <div>
        <h1>{{.Title}}</h1>
        <div class="summary">Guided setup for a scheduled Faktory job.</div>
      </div>
      <nav>
        <a class="button secondary" href="/">Cancel</a>
      </nav>
    </div>
  </header>
  <main class="wiz">
    {{if .Error}}<div class="alert error">{{.Error}}</div>{{end}}
    <form action="/v1/schedules" method="post" id="wiz-form">
      <input type="hidden" name="original_name" value="{{.OriginalName}}">
      <input type="hidden" name="target_type" value="faktory">
      <input type="hidden" name="timeout" value="5s">
      <input type="hidden" name="name" id="wiz-name" value="{{.Schedule.Name}}">

      <div class="wiz-narrative" id="wiz-narrative">
        <span class="wiz-frag visible" id="nf-base">You are creating a Faktory job</span><span class="wiz-frag" id="nf-instance"> on the <b id="nf-instance-val"></b> instance</span><span class="wiz-frag" id="nf-job"> that runs <b id="nf-job-val"></b></span><span class="wiz-frag" id="nf-queue"> on the <b id="nf-queue-val"></b> queue</span><span class="wiz-frag" id="nf-desc">, which <i id="nf-desc-val"></i></span><span class="wiz-frag" id="nf-sched">, triggered <b id="nf-sched-val"></b></span><span class="wiz-frag visible" id="nf-end">.</span>
      </div>

      {{if gt (len .FaktoryInstances) 1}}
      <div class="wiz-step active" data-wiz-step="instance">
        <h2>Which Faktory server?</h2>
        <p class="wiz-hint">Select the Faktory instance this job should be enqueued to.</p>
        <div class="wiz-fields">
          <label class="full">Instance
            <select name="target_faktory_instance" required id="wiz-instance">
              <option value="">Choose an instance</option>
              {{range .FaktoryInstances}}<option value="{{.}}" {{if eq . $.Schedule.Target.Instance}}selected{{end}}>{{.}}</option>{{end}}
            </select>
          </label>
        </div>
      </div>
      {{else}}
      <input type="hidden" name="target_faktory_instance" id="wiz-instance" value="{{index .FaktoryInstances 0}}">
      {{end}}

      <div class="wiz-step{{if le (len .FaktoryInstances) 1}} active{{end}}" data-wiz-step="job">
        <h2>What should it run?</h2>
        <p class="wiz-hint">Enter the Faktory job type your worker is registered to handle.</p>
        <div class="wiz-fields">
          <label class="full">Job Type
            <input name="target_faktory_job_type" required id="wiz-job-type" value="{{.Schedule.Target.JobType}}" placeholder="nightly_sync_job" autocomplete="off">
          </label>
          <label>Queue
            <input name="target_faktory_queue" id="wiz-queue" list="faktory-queues" value="{{.Schedule.Target.Queue}}" placeholder="default">
            <datalist id="faktory-queues"></datalist>
          </label>
          <label>Description <span style="font-weight:400;text-transform:none">(optional)</span>
            <input name="description" id="wiz-desc" value="{{.Schedule.Description}}" placeholder="Syncs data from the warehouse">
          </label>
          <label class="full">Args JSON <span style="font-weight:400;text-transform:none">(optional)</span>
            <textarea name="target_faktory_args" id="wiz-args" style="min-height:64px">{{targetJSONArgs .Schedule.Target}}</textarea>
          </label>
        </div>
      </div>

      <div class="wiz-step" data-wiz-step="schedule-type">
        <h2>When should it run?</h2>
        <p class="wiz-hint">Pick a scheduling strategy.</p>
        <div class="wiz-schedule-cards">
          <button type="button" class="wiz-scard{{if eq .Schedule.ScheduleType "rate"}} selected{{end}}" data-stype="rate">
            <strong>Rate</strong>
            <small>Every N minutes, hours, or days.</small>
          </button>
          <button type="button" class="wiz-scard{{if eq .Schedule.ScheduleType "once"}} selected{{end}}" data-stype="once">
            <strong>Once</strong>
            <small>One time at a specific date and time.</small>
          </button>
          <button type="button" class="wiz-scard{{if or (eq .Schedule.ScheduleType "cron") (eq .Schedule.ScheduleType "")}} selected{{end}}" data-stype="cron">
            <strong>Cron</strong>
            <small>Five-field expression for calendar schedules.</small>
          </button>
        </div>
        <input type="hidden" name="schedule_type" id="wiz-schedule-type" value="{{.Schedule.ScheduleType}}">
      </div>

      <div class="wiz-step" data-wiz-step="schedule-detail">
        <h2>Configure the schedule</h2>
        <p class="wiz-hint" id="wiz-detail-hint">Set the timing details.</p>
        <div class="wiz-fields">
          <label>Timezone
            <select name="timezone" id="wiz-tz">
              {{range .Timezones}}<option value="{{.}}" {{if eq . $.Schedule.Timezone}}selected{{end}}>{{.}}</option>{{end}}
            </select>
          </label>
          <label>Allow concurrent runs
            <select name="allow_concurrent_runs" id="wiz-allow-concurrent">
              <option value="true" {{if allowConcurrent .Schedule}}selected{{end}}>Yes</option>
              <option value="false" {{if not (allowConcurrent .Schedule)}}selected{{end}}>No</option>
            </select>
          </label>
        </div>

        <div class="wiz-fields" id="wiz-rate-fields" style="margin-top:14px;display:none">
          <label>Every
            <input name="rate_interval" type="number" min="1" id="wiz-rate-interval" value="{{.Schedule.RateInterval}}">
          </label>
          <label>Unit
            <select name="rate_unit" id="wiz-rate-unit">
              <option value="minutes" {{if eq .Schedule.RateUnit "minutes"}}selected{{end}}>minutes</option>
              <option value="hours" {{if eq .Schedule.RateUnit "hours"}}selected{{end}}>hours</option>
              <option value="days" {{if eq .Schedule.RateUnit "days"}}selected{{end}}>days</option>
            </select>
          </label>
          <label class="full">Starting from
            <input name="starts_at" type="datetime-local" id="wiz-starts-at" value="{{.Schedule.StartsAt}}">
          </label>
        </div>

        <div class="wiz-fields" id="wiz-once-fields" style="margin-top:14px;display:none">
          <label class="full">Run at
            <input name="run_at" type="datetime-local" id="wiz-run-at" value="{{.Schedule.RunAt}}">
          </label>
        </div>

        <div class="wiz-fields" id="wiz-cron-fields" style="margin-top:14px;display:none">
          <label class="full">Cron expression
            <input name="cron" id="wiz-cron" value="{{.Schedule.Cron}}" placeholder="*/5 * * * *">
          </label>
        </div>
      </div>

      <div class="wiz-step" data-wiz-step="review">
        <h2>Review and create</h2>
        <p class="wiz-hint">Everything look right?</p>
        <table class="wiz-review-table" id="wiz-review-body"></table>
        <div style="margin-top:20px;text-align:right">
          <button type="submit">Create job</button>
        </div>
      </div>

      <div class="wiz-nav" id="wiz-nav">
        <button type="button" class="wiz-back" id="wiz-back" style="display:none">&#8592; Back</button>
        <div class="wiz-progress" id="wiz-dots"></div>
        <button type="button" class="button" id="wiz-next">Continue</button>
      </div>
    </form>
  </main>
  {{template "time_helpers"}}
  <script>
  (function() {
    const form = document.getElementById("wiz-form");
    const steps = Array.from(form.querySelectorAll("[data-wiz-step]"));
    const stepNames = steps.map(s => s.dataset.wizStep);
    const dots = document.getElementById("wiz-dots");
    const backBtn = document.getElementById("wiz-back");
    const nextBtn = document.getElementById("wiz-next");
    const nav = document.getElementById("wiz-nav");
    let cur = stepNames.indexOf(steps.find(s => s.classList.contains("active"))?.dataset.wizStep || stepNames[0]);

    const localDateTime = ClockRelayTime.localDateTime;
    const preferredTimezone = ClockRelayTime.preferredScheduleTimezone();

    // hydrate defaults for new jobs
    const isNew = !form.querySelector("[name='original_name']").value;
    if (isNew) {
      const tz = document.getElementById("wiz-tz");
      ClockRelayTime.selectTimezone(tz, preferredTimezone);
      const now = new Date();
      const sa = document.getElementById("wiz-starts-at");
      if (sa && !sa.value) sa.value = localDateTime(now, preferredTimezone);
      const ra = document.getElementById("wiz-run-at");
      if (ra && !ra.value) ra.value = localDateTime(new Date(now.getTime() + 5*60*1000), preferredTimezone);
    }

    // build dots
    stepNames.forEach(() => {
      const d = document.createElement("span");
      d.className = "wiz-dot";
      dots.appendChild(d);
    });

    function showStep(idx) {
      cur = idx;
      steps.forEach((s, i) => s.classList.toggle("active", i === idx));
      const allDots = dots.querySelectorAll(".wiz-dot");
      allDots.forEach((d, i) => {
        d.className = "wiz-dot" + (i < idx ? " done" : "") + (i === idx ? " current" : "");
      });
      backBtn.style.display = idx === 0 ? "none" : "";
      const isReview = stepNames[idx] === "review";
      nextBtn.style.display = isReview ? "none" : "";
      if (stepNames[idx] === "schedule-detail") syncScheduleFields();
      if (isReview) buildReview();
      updateNarrative();
    }

    // narrative fragments
    function show(id, visible) {
      const el = document.getElementById(id);
      if (el) el.classList.toggle("visible", visible);
    }
    function setText(id, text) {
      const el = document.getElementById(id);
      if (el) el.textContent = text;
    }

    function updateNarrative() {
      const instIdx = stepNames.indexOf("instance");
      const jobIdx = stepNames.indexOf("job");
      const schedIdx = stepNames.indexOf("schedule-type");

      const pastInstance = instIdx === -1 ? cur > 0 : cur > instIdx;
      const pastJob = cur > jobIdx;
      const pastSched = cur > schedIdx;

      const inst = document.getElementById("wiz-instance")?.value || "";
      const jt = document.getElementById("wiz-job-type")?.value || "";
      const q = document.getElementById("wiz-queue")?.value || "default";
      const desc = document.getElementById("wiz-desc")?.value || "";

      show("nf-instance", pastInstance && !!inst);
      setText("nf-instance-val", inst);
      show("nf-job", pastJob && !!jt);
      setText("nf-job-val", jt);
      show("nf-queue", pastJob && !!jt);
      setText("nf-queue-val", q || "default");
      show("nf-desc", pastJob && !!desc);
      setText("nf-desc-val", desc);
      show("nf-sched", pastSched);
      setText("nf-sched-val", scheduleLabel());
    }

    function scheduleLabel() {
      const st = document.getElementById("wiz-schedule-type")?.value;
      if (st === "rate") {
        const n = document.getElementById("wiz-rate-interval")?.value || "?";
        const u = document.getElementById("wiz-rate-unit")?.value || "minutes";
        return "every " + n + " " + (n === "1" ? u.replace(/s$/, "") : u);
      }
      if (st === "once") {
        const ra = document.getElementById("wiz-run-at")?.value;
        if (ra) { try { return "once at " + new Date(ra).toLocaleString(undefined, {dateStyle:"medium",timeStyle:"short"}); } catch(_){} }
        return "once";
      }
      if (st === "cron") {
        const c = document.getElementById("wiz-cron")?.value;
        return c ? "on cron " + c : "on a cron schedule";
      }
      return "";
    }

    // auto-derive name from job_type
    function syncName() {
      if (!isNew) return;
      const jt = document.getElementById("wiz-job-type")?.value || "";
      document.getElementById("wiz-name").value = jt.replace(/_/g, "-");
    }

    // queue fetching
    async function fetchQueues(instance) {
      if (!instance) return;
      try {
        const resp = await fetch("/v1/faktory/queues?instance=" + encodeURIComponent(instance));
        if (!resp.ok) return;
        const queues = await resp.json();
        const dl = document.getElementById("faktory-queues");
        if (!dl) return;
        while (dl.firstChild) dl.removeChild(dl.firstChild);
        queues.forEach(q => { const o = document.createElement("option"); o.value = q; dl.appendChild(o); });
      } catch(_) {}
    }

    // schedule type cards
    form.querySelectorAll(".wiz-scard").forEach(card => {
      card.addEventListener("click", () => {
        form.querySelectorAll(".wiz-scard").forEach(c => c.classList.remove("selected"));
        card.classList.add("selected");
        document.getElementById("wiz-schedule-type").value = card.dataset.stype;
      });
    });

    function syncScheduleFields() {
      const st = document.getElementById("wiz-schedule-type")?.value;
      document.getElementById("wiz-rate-fields").style.display = st === "rate" ? "" : "none";
      document.getElementById("wiz-once-fields").style.display = st === "once" ? "" : "none";
      document.getElementById("wiz-cron-fields").style.display = st === "cron" ? "" : "none";
      const hints = { rate: "Set the interval and starting time.", once: "Choose when to run.", cron: "Enter a five-field cron expression." };
      document.getElementById("wiz-detail-hint").textContent = hints[st] || "Set the timing details.";
    }

    // validation per step
    function validateStep(idx) {
      const name = stepNames[idx];
      if (name === "instance") {
        return !!document.getElementById("wiz-instance")?.value;
      }
      if (name === "job") {
        const jt = document.getElementById("wiz-job-type")?.value?.trim();
        return !!jt;
      }
      if (name === "schedule-type") {
        return !!document.getElementById("wiz-schedule-type")?.value;
      }
      if (name === "schedule-detail") {
        const st = document.getElementById("wiz-schedule-type")?.value;
        if (st === "rate") return !!document.getElementById("wiz-rate-interval")?.value;
        if (st === "once") return !!document.getElementById("wiz-run-at")?.value;
        if (st === "cron") return !!document.getElementById("wiz-cron")?.value;
      }
      return true;
    }

    // review table
    function buildReview() {
      const inst = document.getElementById("wiz-instance")?.value || "";
      const jt = document.getElementById("wiz-job-type")?.value || "";
      const q = document.getElementById("wiz-queue")?.value || "default";
      const desc = document.getElementById("wiz-desc")?.value || "";
      const args = document.getElementById("wiz-args")?.value?.trim() || "[]";
      const tz = document.getElementById("wiz-tz")?.value || "UTC";
      const allowConcurrent = document.getElementById("wiz-allow-concurrent")?.value === "true";

      const rows = [
        ["Instance", inst],
        ["Job type", "<code>" + escHtml(jt) + "</code>"],
        ["Queue", q],
      ];
      if (desc) rows.push(["Description", escHtml(desc)]);
      rows.push(["Schedule", scheduleLabel()]);
      rows.push(["Timezone", tz]);
      rows.push(["Concurrent runs", allowConcurrent ? "Allowed" : "Not allowed"]);
      if (args && args !== "[]") rows.push(["Args", "<code>" + escHtml(args) + "</code>"]);

      const tbody = document.getElementById("wiz-review-body");
      while (tbody.firstChild) tbody.removeChild(tbody.firstChild);
      rows.forEach(([label, val]) => {
        const tr = document.createElement("tr");
        const td1 = document.createElement("td");
        td1.textContent = label;
        const td2 = document.createElement("td");
        td2.innerHTML = val;
        tr.appendChild(td1);
        tr.appendChild(td2);
        tbody.appendChild(tr);
      });
    }

    function escHtml(s) {
      const d = document.createElement("div");
      d.textContent = s;
      return d.innerHTML;
    }

    // navigation
    nextBtn.addEventListener("click", () => {
      if (!validateStep(cur)) {
        steps[cur].querySelector("input,select")?.focus();
        return;
      }
      syncName();
      if (stepNames[cur] === "schedule-type") syncScheduleFields();
      if (cur < steps.length - 1) showStep(cur + 1);
    });
    backBtn.addEventListener("click", () => { if (cur > 0) showStep(cur - 1); });

    // live narrative updates
    ["wiz-instance","wiz-job-type","wiz-queue","wiz-desc","wiz-rate-interval","wiz-rate-unit","wiz-cron","wiz-run-at"].forEach(id => {
      const el = document.getElementById(id);
      if (el) el.addEventListener("input", updateNarrative);
      if (el) el.addEventListener("change", updateNarrative);
    });

    // instance change => fetch queues
    const instEl = document.getElementById("wiz-instance");
    if (instEl) {
      if (instEl.value) fetchQueues(instEl.value);
      instEl.addEventListener("change", () => fetchQueues(instEl.value));
    }

    showStep(cur);
  })();
  </script>
</body>
</html>{{end}}

{{define "styles"}}
  :root {
    color-scheme: light;
    --bg: #f7f8fa;
    --panel: #ffffff;
    --text: #18202a;
    --muted: #687384;
    --line: #d8dee8;
    --accent: #127c75;
    --failed: #b42318;
    --ok: #137333;
    --running: #8a5a00;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0;
    background: var(--bg);
    color: var(--text);
    font: 14px/1.45 system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
  }
  header {
    border-bottom: 1px solid var(--line);
    background: var(--panel);
    padding: 18px 24px;
  }
  .header-inner {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    max-width: 1180px;
    margin: 0 auto;
  }
  h1, h2 { margin: 0; letter-spacing: 0; }
  h1 { font-size: 22px; }
  h2 { font-size: 16px; }
  main {
    max-width: 1180px;
    margin: 0 auto;
    padding: 24px;
    display: grid;
    gap: 24px;
  }
  .summary {
    color: var(--muted);
    margin-top: 4px;
  }
  section {
    background: var(--panel);
    border: 1px solid var(--line);
    border-radius: 8px;
    overflow: hidden;
  }
  .table-section {
    overflow: visible;
    position: relative;
  }
  .jobs-section {
    z-index: 20;
  }
  .runs-section {
    z-index: 10;
  }
  .section-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 14px 16px;
    border-bottom: 1px solid var(--line);
  }
  .section-actions {
    display: flex;
    align-items: center;
    gap: 10px;
    white-space: nowrap;
  }
  table {
    width: 100%;
    border-collapse: collapse;
  }
  th, td {
    padding: 12px 16px;
    border-bottom: 1px solid var(--line);
    text-align: left;
    vertical-align: top;
  }
  th {
    color: var(--muted);
    font-size: 12px;
    font-weight: 600;
    text-transform: uppercase;
  }
  tr:last-child td { border-bottom: 0; }
  code {
    background: #eef1f5;
    border-radius: 4px;
    padding: 2px 5px;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 12px;
  }
  button, .button {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-height: 32px;
    border: 1px solid var(--accent);
    background: var(--accent);
    color: #fff;
    border-radius: 6px;
    padding: 7px 10px;
    cursor: pointer;
    font-weight: 600;
    text-decoration: none;
    font: inherit;
  }
  button:hover, .button:hover { filter: brightness(0.95); }
  .button.secondary, button.secondary {
    background: #fff;
    color: var(--accent);
  }
  button.compact, .button.compact {
    min-height: 28px;
    padding: 4px 8px;
  }
  nav { display: flex; align-items: center; gap: 8px; }
  .toolbar-field {
    align-items: start;
    color: var(--muted);
    display: grid;
    font-size: 11px;
    font-weight: 700;
    gap: 3px;
    line-height: 1.1;
    min-width: 120px;
    text-transform: uppercase;
  }
  .toolbar-field select {
    text-transform: none;
  }
  select.compact {
    font: inherit;
    font-size: 13px;
    min-height: 28px;
    padding: 4px 6px;
    border: 1px solid var(--line);
    border-radius: 6px;
    background: #fff;
    color: var(--text);
    cursor: pointer;
  }
  button.danger {
    border-color: var(--failed);
    background: var(--failed);
  }
  form.inline {
    display: inline-flex;
  }
  .actions {
    white-space: nowrap;
    display: flex;
    gap: 6px;
    align-items: center;
  }
  .overflow-menu {
    position: relative;
  }
  .overflow-toggle {
    min-width: 36px;
    padding: 4px 0;
    font-weight: 900;
    letter-spacing: 2px;
  }
  .overflow-items {
    display: none;
    position: absolute;
    right: 0;
    top: 100%;
    margin-top: 4px;
    background: #fff;
    border: 1px solid var(--line);
    border-radius: 8px;
    box-shadow: 0 4px 12px rgba(0,0,0,.1);
    min-width: 120px;
    z-index: 50;
    padding: 4px 0;
  }
  .overflow-menu.open .overflow-items {
    display: block;
  }
  .overflow-items a,
  .overflow-items button {
    display: block;
    width: 100%;
    padding: 8px 14px;
    background: none;
    border: none;
    border-radius: 0;
    color: var(--text);
    font: inherit;
    font-size: 14px;
    text-align: left;
    text-decoration: none;
    cursor: pointer;
    min-height: auto;
  }
  .overflow-items a:hover,
  .overflow-items button:hover {
    background: #f5f6f8;
  }
  .overflow-items .overflow-danger {
    color: var(--failed);
  }
  .job-form {
    display: grid;
    gap: 18px;
  }
  .form-card {
    background: var(--panel);
    border: 1px solid var(--line);
    border-radius: 8px;
    overflow: hidden;
  }
  .form-card-head {
    border-bottom: 1px solid var(--line);
    padding: 16px 18px;
  }
  .form-card-head p {
    color: var(--muted);
    margin: 5px 0 0;
  }
  .form-grid {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 12px;
    padding: 18px;
  }
  label {
    display: grid;
    gap: 5px;
    color: var(--muted);
    font-size: 12px;
    font-weight: 700;
    text-transform: uppercase;
  }
  input, select, textarea {
    width: 100%;
    border: 1px solid var(--line);
    border-radius: 6px;
    padding: 8px 9px;
    color: var(--text);
    background: #fff;
    font: 14px/1.35 system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
	  }
	  .field-hint {
	    color: var(--muted);
	    font-size: 12px;
	    font-weight: 500;
	    text-transform: none;
	  }
	  .field-hint strong {
	    color: var(--text);
	    font-weight: 700;
	  }
	  textarea {
    min-height: 92px;
    resize: vertical;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 12px;
  }
  .span-2 { grid-column: span 2; }
  .span-3 { grid-column: span 3; }
  .span-4 { grid-column: span 4; }
  .panel-copy h2 {
    font-size: 15px;
  }
  fieldset {
    border: 0;
    margin: 0;
    padding: 0;
  }
  legend {
    color: var(--muted);
    font-size: 13px;
    margin-bottom: 10px;
  }
  .type-picker {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 12px;
  }
  .type-option {
    display: grid;
    grid-template-columns: auto 1fr;
    align-items: start;
    gap: 10px;
    border: 1px solid var(--line);
    border-radius: 8px;
    padding: 11px 12px;
    color: var(--text);
    text-transform: none;
    font-size: 14px;
    font-weight: 400;
  }
  .type-option input {
    width: auto;
    margin-top: 3px;
  }
  .type-option small {
    display: block;
    color: var(--muted);
    font-size: 13px;
    font-weight: 400;
    margin-top: 3px;
  }
  .mode-panel {
    border-top: 1px solid var(--line);
    padding-top: 14px;
  }
  [data-target-panel][hidden],
  [data-schedule-panel][hidden] {
    display: none;
  }
  .panel-copy {
    margin-bottom: 12px;
  }
  .panel-copy p {
    color: var(--muted);
    margin: 5px 0 0;
  }
  .nested-grid {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 12px;
  }
  .schedule-preview {
    align-self: end;
    border: 1px solid var(--line);
    border-radius: 8px;
    padding: 10px;
    background: #fbfcfd;
  }
  .schedule-preview span {
    display: block;
    color: var(--muted);
    font-size: 12px;
    font-weight: 700;
    margin-bottom: 5px;
    text-transform: uppercase;
  }
  .form-actions {
    display: flex;
    gap: 8px;
    align-items: center;
    justify-content: flex-end;
    padding: 2px 0 24px;
  }
  .alert {
    border: 1px solid var(--line);
    border-radius: 8px;
    padding: 10px 12px;
    background: #fff;
  }
  .alert.error {
    color: var(--failed);
    border-color: #f3b8b2;
    background: #fff7f6;
  }
  .alert.notice {
    color: var(--ok);
    border-color: #b9dfc4;
    background: #f3fbf5;
  }
  .muted { color: var(--muted); }
  .status {
    display: inline-block;
    border-radius: 999px;
    padding: 2px 8px;
    font-size: 12px;
    font-weight: 700;
    background: #eef1f5;
  }
  .status-succeeded { color: var(--ok); background: #e7f4ea; }
  .status-running, .status-queued { color: var(--running); background: #fff4d6; }
  .status-failed { color: var(--failed); background: #fce8e6; }
  .status-skipped { color: #5d6674; background: #eef1f5; }
  .status-paused { color: #7c6b2f; background: #fef9e7; }
  .row-paused { opacity: 0.55; }
  .empty {
    padding: 24px 16px;
    color: var(--muted);
  }
  .output {
    max-width: 420px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .output-dialog {
    width: min(820px, calc(100vw - 32px));
    max-height: calc(100vh - 64px);
    border: 1px solid var(--line);
    border-radius: 8px;
    padding: 0;
    color: var(--text);
    background: var(--panel);
    box-shadow: 0 24px 80px rgba(24, 32, 42, 0.22);
  }
  .output-dialog::backdrop {
    background: rgba(24, 32, 42, 0.32);
  }
  .dialog-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
    border-bottom: 1px solid var(--line);
    padding: 14px 16px;
  }
  .output-dialog pre {
    max-height: min(620px, calc(100vh - 180px));
    margin: 0;
    overflow: auto;
    white-space: pre-wrap;
    word-break: break-word;
    padding: 16px;
    background: #fbfcfd;
    color: var(--text);
    font: 12px/1.5 ui-monospace, SFMono-Regular, Menlo, monospace;
  }
  .picker-grid {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 16px;
  }
  .picker-card {
    display: block;
    background: var(--panel);
    border: 2px solid var(--line);
    border-radius: 10px;
    padding: 24px 20px;
    text-decoration: none;
    color: var(--text);
    transition: border-color 0.15s, box-shadow 0.15s;
    position: relative;
  }
  .picker-card:hover {
    border-color: var(--accent);
    box-shadow: 0 2px 12px rgba(18, 124, 117, 0.10);
  }
  .picker-card strong {
    display: block;
    font-size: 17px;
    margin-bottom: 8px;
  }
  .picker-card p {
    color: var(--muted);
    margin: 0;
    font-size: 14px;
    line-height: 1.45;
  }
  .picker-card.disabled {
    opacity: 0.55;
    cursor: default;
    pointer-events: none;
  }
  .picker-badge {
    display: inline-block;
    margin-top: 12px;
    background: #eef1f5;
    color: var(--muted);
    border-radius: 999px;
    padding: 3px 10px;
    font-size: 12px;
    font-weight: 600;
  }
  @media (max-width: 760px) {
    header { padding: 16px; }
    .header-inner { align-items: flex-start; flex-direction: column; }
    main { padding: 12px; }
    .section-head, .section-actions { align-items: flex-start; flex-direction: column; }
    .form-grid { grid-template-columns: 1fr; }
    .nested-grid, .type-picker, .picker-grid { grid-template-columns: 1fr; }
    .span-2, .span-3, .span-4, .form-actions { grid-column: span 1; }
    table, thead, tbody, tr, th, td { display: block; }
    thead { display: none; }
    tr { border-bottom: 1px solid var(--line); padding: 10px 0; }
    td { border: 0; padding: 6px 16px; }
    td::before {
      content: attr(data-label);
      display: block;
      color: var(--muted);
      font-size: 12px;
      font-weight: 600;
      text-transform: uppercase;
    }
    .actions {
      white-space: normal;
      flex-wrap: wrap;
    }
  }
{{end}}`
