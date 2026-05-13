package target

import (
	"context"
	"testing"
	"time"

	faktory "github.com/contribsys/faktory/client"
	"github.com/johnnycon/clock-relay/internal/config"
)

func TestBuildFaktoryJob(t *testing.T) {
	schedule := config.ScheduleConfig{
		Name: "faktory-smoke",
		Target: config.TargetConfig{
			Type:    "faktory",
			Queue:   "critical",
			JobType: "smoke_job",
			Args:    []any{"acct_123", map[string]any{"kind": "smoke"}},
		},
	}

	job, err := buildFaktoryJob(schedule)
	if err != nil {
		t.Fatal(err)
	}
	if job.Type != "smoke_job" {
		t.Fatalf("expected job type, got %q", job.Type)
	}
	if job.Queue != "critical" {
		t.Fatalf("expected queue, got %q", job.Queue)
	}
	if len(job.Args) != 2 {
		t.Fatalf("expected args to pass through, got %#v", job.Args)
	}
	if len(job.Custom) != 0 {
		t.Fatalf("expected no custom metadata by default, got %#v", job.Custom)
	}
}

func TestFaktoryTargetStructuredOutput(t *testing.T) {
	job := faktory.NewJob("smoke_job")
	job.Jid = "abc123"
	job.Queue = "default"
	result := faktoryTargetResult(job)

	if result.StructuredOutput["raw"] != "faktory jid=abc123 queue=default job_type=smoke_job" {
		t.Fatalf("expected raw structured output, got %#v", result.StructuredOutput)
	}
	if result.StructuredOutput["provider"] != "faktory" {
		t.Fatalf("expected provider structured output, got %#v", result.StructuredOutput)
	}
	if result.StructuredOutput["jid"] != "abc123" {
		t.Fatalf("expected jid structured output, got %#v", result.StructuredOutput)
	}
	if result.StructuredOutput["queue"] != "default" {
		t.Fatalf("expected queue structured output, got %#v", result.StructuredOutput)
	}
	if result.StructuredOutput["job_type"] != "smoke_job" {
		t.Fatalf("expected job_type structured output, got %#v", result.StructuredOutput)
	}
}

func TestBuildFaktoryJobUsesEmptyArgsArray(t *testing.T) {
	job, err := buildFaktoryJob(config.ScheduleConfig{
		Name: "no-args",
		Target: config.TargetConfig{
			Type:    "faktory",
			JobType: "no_args_job",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.Args == nil {
		t.Fatal("expected empty args slice, got nil")
	}
	if len(job.Args) != 0 {
		t.Fatalf("expected no args, got %#v", job.Args)
	}
}

func TestFaktoryServerParsesURL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	server, err := faktoryServer(ctx, config.FaktoryInstance{URL: "tcp://faktory:7419"})
	if err != nil {
		t.Fatal(err)
	}
	if server.Network != "tcp" {
		t.Fatalf("expected tcp network, got %q", server.Network)
	}
	if server.Address != "faktory:7419" {
		t.Fatalf("expected address, got %q", server.Address)
	}
	if server.Password != "" {
		t.Fatalf("expected no password, got %q", server.Password)
	}
}

func TestFaktoryServerAcceptsBareAddress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	server, err := faktoryServer(ctx, config.FaktoryInstance{URL: "faktory:7419"})
	if err != nil {
		t.Fatal(err)
	}
	if server.Network != "tcp" {
		t.Fatalf("expected tcp network, got %q", server.Network)
	}
	if server.Address != "faktory:7419" {
		t.Fatalf("expected address, got %q", server.Address)
	}
}

func TestFaktoryServerUsesPasswordEnv(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	t.Setenv("TEST_FAKTORY_PW", "env-secret")

	server, err := faktoryServer(ctx, config.FaktoryInstance{
		URL:         "tcp://faktory:7419",
		PasswordEnv: "TEST_FAKTORY_PW",
	})
	if err != nil {
		t.Fatal(err)
	}
	if server.Password != "env-secret" {
		t.Fatalf("expected env password, got %q", server.Password)
	}
}

func TestFaktoryServerNoPasswordWithoutPasswordEnv(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	server, err := faktoryServer(ctx, config.FaktoryInstance{URL: "tcp://faktory:7419"})
	if err != nil {
		t.Fatal(err)
	}
	if server.Password != "" {
		t.Fatalf("expected no password, got %q", server.Password)
	}
}

func TestFaktoryServerEmptyPasswordEnvValueMeansNoPassword(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	t.Setenv("TEST_EMPTY_PW", "")

	server, err := faktoryServer(ctx, config.FaktoryInstance{
		URL:         "tcp://faktory:7419",
		PasswordEnv: "TEST_EMPTY_PW",
	})
	if err != nil {
		t.Fatal(err)
	}
	if server.Password != "" {
		t.Fatalf("expected no password for empty env var, got %q", server.Password)
	}
}

func TestResolveFaktoryInstance(t *testing.T) {
	instances := []config.FaktoryInstance{
		{Name: "prod", URL: "tcp://prod-faktory:7419", PasswordEnv: "PROD_FAKTORY_PW"},
		{Name: "staging", URL: "tcp://staging-faktory:7419"},
	}

	t.Run("resolves named instance", func(t *testing.T) {
		schedule := config.ScheduleConfig{Target: config.TargetConfig{Instance: "prod"}}
		inst, err := resolveFaktoryInstance(schedule, instances)
		if err != nil {
			t.Fatal(err)
		}
		if inst.URL != "tcp://prod-faktory:7419" {
			t.Fatalf("expected prod URL, got %q", inst.URL)
		}
		if inst.PasswordEnv != "PROD_FAKTORY_PW" {
			t.Fatalf("expected prod password_env, got %q", inst.PasswordEnv)
		}
	})

	t.Run("errors on unknown instance", func(t *testing.T) {
		schedule := config.ScheduleConfig{Target: config.TargetConfig{Instance: "nonexistent"}}
		_, err := resolveFaktoryInstance(schedule, instances)
		if err == nil {
			t.Fatal("expected error for unknown instance")
		}
	})

	t.Run("errors on missing instance", func(t *testing.T) {
		schedule := config.ScheduleConfig{Target: config.TargetConfig{}}
		_, err := resolveFaktoryInstance(schedule, instances)
		if err == nil {
			t.Fatal("expected error for missing instance")
		}
	})
}
