package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxOutputBytes = 64 * 1024

var httpClient = &http.Client{Timeout: 30 * time.Second}

type TargetResult struct {
	StructuredOutput map[string]any
}

func TriggerTarget(ctx context.Context, schedule ScheduleConfig, run Run, faktoryInstances []FaktoryInstance) (TargetResult, error) {
	switch schedule.Target.Type {
	case "http":
		return triggerHTTP(ctx, schedule, run)
	case "faktory":
		return triggerFaktory(ctx, schedule, faktoryInstances)
	default:
		return TargetResult{}, ConfigError("unsupported target type: " + schedule.Target.Type)
	}
}

func triggerHTTP(ctx context.Context, schedule ScheduleConfig, run Run) (TargetResult, error) {
	body := schedule.Target.Body
	if body == nil {
		body = map[string]any{
			"run_id":        run.ID,
			"schedule_name": schedule.Name,
			"scheduled_at":  run.ScheduledAt,
			"triggered_by":  run.TriggeredBy,
		}
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return TargetResult{}, err
	}

	req, err := http.NewRequestWithContext(ctx, schedule.Target.Method, schedule.Target.URL, bytes.NewReader(raw))
	if err != nil {
		return TargetResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "clock-relay/0")
	for key, value := range schedule.Target.Headers {
		req.Header.Set(key, value)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return TargetResult{}, err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxOutputBytes))
	if err != nil {
		return TargetResult{}, err
	}
	output := strings.TrimSpace(string(responseBody))
	result := TargetResult{StructuredOutput: map[string]any{
		"raw":         output,
		"status_code": resp.StatusCode,
	}}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, fmt.Errorf("http target returned %s", resp.Status)
	}
	return result, nil
}

