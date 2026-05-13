package relay

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	faktory "github.com/contribsys/faktory/client"
)

func triggerFaktory(ctx context.Context, schedule ScheduleConfig, instances []FaktoryInstance) (TargetResult, error) {
	if err := ctx.Err(); err != nil {
		return TargetResult{}, err
	}

	instance, err := resolveFaktoryInstance(schedule, instances)
	if err != nil {
		return TargetResult{}, err
	}
	job, err := buildFaktoryJob(schedule)
	if err != nil {
		return TargetResult{}, err
	}
	client, err := openFaktoryClient(ctx, instance)
	if err != nil {
		return TargetResult{}, err
	}
	defer client.Close()

	if err := client.Push(job); err != nil {
		return TargetResult{}, err
	}
	return faktoryTargetResult(job), nil
}

func resolveFaktoryInstance(schedule ScheduleConfig, instances []FaktoryInstance) (FaktoryInstance, error) {
	for _, inst := range instances {
		if inst.Name == schedule.Target.Instance {
			return inst, nil
		}
	}
	return FaktoryInstance{}, ConfigError("unknown faktory instance: " + schedule.Target.Instance)
}

func faktoryTargetResult(job *faktory.Job) TargetResult {
	return TargetResult{
		StructuredOutput: map[string]any{
			"raw":      fmt.Sprintf("faktory jid=%s queue=%s job_type=%s", job.Jid, job.Queue, job.Type),
			"provider": "faktory",
			"jid":      job.Jid,
			"queue":    job.Queue,
			"job_type": job.Type,
		},
	}
}

func buildFaktoryJob(schedule ScheduleConfig) (*faktory.Job, error) {
	if schedule.Target.JobType == "" {
		return nil, ConfigError("faktory target job_type is required")
	}
	queue := schedule.Target.Queue
	if queue == "" {
		queue = "default"
	}
	args := schedule.Target.Args
	if args == nil {
		args = []any{}
	}
	job := faktory.NewJob(schedule.Target.JobType, args...)
	job.Queue = queue
	return job, nil
}

func openFaktoryClient(ctx context.Context, instance FaktoryInstance) (*faktory.Client, error) {
	server, err := faktoryServer(ctx, instance)
	if err != nil {
		return nil, err
	}
	return server.Open()
}

func faktoryServer(ctx context.Context, instance FaktoryInstance) (*faktory.Server, error) {
	server := faktory.DefaultServer()
	if deadline, ok := ctx.Deadline(); ok {
		if timeout := time.Until(deadline); timeout > 0 {
			server.Timeout = timeout
		}
	}
	if !strings.Contains(instance.URL, "://") {
		server.Network = "tcp"
		server.Address = instance.URL
		server.Password = faktoryPassword(instance)
		return server, nil
	}

	parsed, err := url.Parse(instance.URL)
	if err != nil {
		return nil, fmt.Errorf("parse faktory url: %w", err)
	}
	switch parsed.Scheme {
	case "tcp", "tcp+tls":
		server.Network = parsed.Scheme
	default:
		return nil, ConfigError("unsupported faktory url scheme: " + parsed.Scheme)
	}
	server.Address = parsed.Host
	if server.Address == "" {
		return nil, ConfigError("faktory url must include host and port")
	}
	if server.Network == "tcp+tls" && server.TLS == nil {
		server.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	server.Password = faktoryPassword(instance)
	return server, nil
}

func faktoryPassword(instance FaktoryInstance) string {
	if instance.PasswordEnv == "" {
		return ""
	}
	return os.Getenv(instance.PasswordEnv)
}

func faktoryQueues(ctx context.Context, instance FaktoryInstance) ([]string, error) {
	client, err := openFaktoryClient(ctx, instance)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	sizes, err := client.QueueSizes()
	if err != nil {
		return nil, err
	}
	queues := make([]string, 0, len(sizes))
	for name := range sizes {
		queues = append(queues, name)
	}
	slices.Sort(queues)
	return queues, nil
}
