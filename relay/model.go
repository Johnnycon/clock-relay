package relay

import "time"

type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
	RunSkipped   RunStatus = "skipped"
)

type Run struct {
	ID               string         `json:"id"`
	ScheduleName     string         `json:"schedule_name"`
	TargetType       string         `json:"target_type"`
	TriggeredBy      string         `json:"triggered_by"`
	Status           RunStatus      `json:"status"`
	ScheduledAt      time.Time      `json:"scheduled_at"`
	StartedAt        time.Time      `json:"started_at"`
	FinishedAt       *time.Time     `json:"finished_at,omitempty"`
	StructuredOutput map[string]any `json:"structured_output,omitempty"`
	Error            string         `json:"error,omitempty"`
}
