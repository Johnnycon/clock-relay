# Clock Relay

> **Pre-alpha software.** Clock Relay is under active development and is not ready for production use. APIs, config formats, and storage schemas will change without notice. Use it to explore and experiment, but expect breaking changes.

Clock Relay is a small self-hosted scheduler for infrastructure jobs. It owns schedule timing, run history, visibility, and trigger delivery; your applications or worker systems own the actual work.

The first prototype supports:

- YAML-defined schedules.
- UI-managed schedule creation, editing, deletion, and pause/resume.
- Explicit one-time, rate-based, and cron-based schedules.
- IANA timezone dropdowns for schedule timezone selection.
- HTTP webhook targets.
- Native Faktory enqueue targets.
- UI create/edit support for HTTP and Faktory jobs.
- Durable local run history with bbolt.
- A minimal web UI with a job list, run log, and dedicated add/edit screens.
- Lightweight live refresh for the job list and run log.
- Run log clearing from the UI or API.
- Configurable run log retention by record count and age.
- JSON APIs for health, schedules, and runs.

## Quick Start

```sh
go run ./cmd/clock-relay --config clock-relay.example.yaml
```

Open http://localhost:9808.

Run tests with:

```sh
go test ./...
```

## Docker

For local development:

```sh
docker compose up --build
```

The compose file mounts `clock-relay-data` at `/app/data` so schedules and run history survive container restarts. The app writes process logs to stdout/stderr and does not create its own application log file.

Published release images are built by GitHub Actions and pushed to the GitHub Container Registry:

```text
ghcr.io/johnnycon/clock-relay:<version>
```

Use exact version tags from downstream projects:

```yaml
services:
  clock-relay:
    image: ghcr.io/johnnycon/clock-relay:0.0.1
    ports:
      - "9808:9808"
    volumes:
      - clock-relay-data:/app/data
    command: ["clock-relay", "--config", "/app/clock-relay.yaml"]

volumes:
  clock-relay-data:
```

For Docker Swarm, use the same image reference:

```yaml
services:
  clock-relay:
    image: ghcr.io/johnnycon/clock-relay:0.0.1
    ports:
      - target: 9808
        published: 9808
        protocol: tcp
        mode: ingress
    volumes:
      - clock-relay-data:/app/data
    command: ["clock-relay", "--config", "/app/clock-relay.yaml"]

volumes:
  clock-relay-data:
```

Release images are published from version tags. To publish `0.0.1`:

```sh
git tag v0.0.1
git push origin v0.0.1
```

The resulting image tag is `ghcr.io/johnnycon/clock-relay:0.0.1`.

Release builds embed their version metadata:

```sh
docker run --rm ghcr.io/johnnycon/clock-relay:0.0.1 clock-relay --version
```

## License

Clock Relay is licensed under the MIT License. See `LICENSE` for the project
license and `THIRD_PARTY_NOTICES.md` for dependency license notes.

## Current Shape

Clock Relay has one Go binary in `cmd/clock-relay`. The main implementation lives under `relay`:

- `config.go`: YAML/API config structs, validation, and defaults.
- `engine.go`: schedule registration, manual triggers, runtime job CRUD, and run execution.
- `store.go`: store interface plus memory and bbolt implementations.
- `target.go`: HTTP target.
- `target_faktory.go`: native Faktory enqueue target.
- `http.go`: JSON API routes plus the server-rendered UI.
- `model.go`: run model and statuses.

Persisted jobs and runs are stored in bbolt by default. YAML schedules can be used to seed a fresh store, but the bundled local configs start with no jobs. After jobs are created through the UI/API, bbolt is the source of truth.

## Example Schedule

```yaml
schedules:
  - name: heartbeat
    description: Calls a local app endpoint every minute.
    schedule_type: rate
    starts_at: "2026-05-08T10:30"
    timezone: UTC
    rate_interval: 1
    rate_unit: minutes
    timeout: 10s
    allow_concurrent_runs: false
    target:
      type: http
      url: http://host.docker.internal:3000/internal/heartbeat
      method: POST
```

## Faktory Target

Clock Relay can enqueue native Faktory jobs. A successful Clock Relay run means Faktory accepted the job; Faktory and its workers own execution, retries, and failure handling after enqueue. Clock Relay records the Faktory JID in the run's `structured_output`.

Faktory jobs can be created and edited in the UI. Args are entered as a JSON array so workers can receive structured values. Use `[]` when the worker does not need arguments; Clock Relay still sends an empty Faktory `args` array because Faktory requires the field.

Example Faktory job types use lower `snake_case` names such as `smoke_job`, `say_hello`, and `meal_reminder`.

```yaml
schedules:
  - name: faktory-smoke
    schedule_type: cron
    cron: "0 0 1 1 *"
    timezone: UTC
    timeout: 10s
    target:
      type: faktory
      url: tcp://faktory:7419
      queue: default
      job_type: smoke_job
      args:
        - account_id: acct_123
```

See `examples/faktory` for a Docker Compose smoke test with a real Faktory server and worker. The bundled Faktory config starts empty; the smoke test creates its temporary `faktory-smoke` schedule through the API before triggering it.

The example runner intentionally uses separate Faktory worker managers for `default` and `reminders` so queue isolation and per-worker concurrency can be tested manually with jobs you create in the UI.

## Run Output

Runs store target results in `structured_output`. This is the canonical result field for both machine-readable provider details and unstructured target text.

HTTP targets store the response body under `structured_output.raw`:

```json
{
  "target_type": "http",
  "structured_output": {
    "raw": "ok",
    "status_code": 200
  }
}
```

Faktory stores the enqueue details as structured fields and also includes `raw` for simple UI display:

```json
{
  "target_type": "faktory",
  "structured_output": {
    "raw": "faktory jid=abc123 queue=default job_type=smoke_job",
    "provider": "faktory",
    "jid": "abc123",
    "queue": "default",
    "job_type": "smoke_job"
  }
}
```

## Run Retention

Jobs are persisted without a built-in count limit, but run history is bounded by `run_retention`:

```yaml
run_retention:
  max_records: 10000
  max_age_days: 30
```

Clock Relay prunes completed run records on startup and then periodically after finalized runs. Running records are preserved and may temporarily exceed the configured limits. The bbolt store maintains run indexes so routine pruning, recent-run reads, and running-run checks do not need to scan the full run history. Deleting records bounds active run data, but bbolt does not guarantee the database file immediately shrinks on disk. Clearing the run log deletes all run records.

## Run Logging

Clock Relay writes process logs to stdout/stderr. Completed and skipped runs are also emitted as structured stdout events by default:

```yaml
run_logging:
  stdout: summary
```

Supported values are `off`, `summary`, and `full`. `summary` includes run identity, status, timing, errors, and safe provider metadata such as HTTP status codes or Faktory JIDs. `full` also includes the complete `structured_output`, including raw HTTP response bodies; use it only when your deployment intentionally ships that potentially sensitive or large data to external logs.

## API

- `GET /healthz`
- `GET /v1/schedules`
- `POST /v1/schedules`
- `GET /v1/runs?limit=100`
- `POST /v1/runs/clear`
- `POST /v1/schedules/{name}/run`
- `POST /v1/schedules/{name}/pause`
- `POST /v1/schedules/{name}/delete`
- `GET /v1/faktory/queues?instance={name}`

## UI

- `/` lists jobs and the recent run log. Use `?job={name}` to filter the run log by job.
- `/schedules/new` opens the add job screen.
- `/schedules/{name}/edit` opens the edit job screen.

The index page refreshes the Jobs and Run Log sections every few seconds while the tab is visible. This is intentionally simple polling for a single-user operational screen, not websocket/SSE infrastructure.

## Schedule Types

Clock Relay stores schedule intent explicitly with `schedule_type`:

- `once`: run one time at `run_at` in the selected IANA `timezone`. After Clock Relay creates the run attempt, it persists `completed_at` and keeps the job visible.
- `rate`: run every `rate_interval` `rate_unit` from `starts_at`. Units are `minutes`, `hours`, or `days`; `days` means a fixed 24-hour interval.
- `cron`: run when a five-field cron expression matches in `timezone`.

Existing schedules without `schedule_type` are treated as `cron`.

The web UI has a "My timezone" control that defaults to the browser's IANA timezone when available. New jobs default to that saved timezone, including their initial date/time fields, and the selected schedule timezone is stored on each job. Config/API-created jobs default to `UTC` unless `timezone` is supplied. UI timestamps are shown in the user's selected timezone, with schedule timezone context shown when it differs.

## Design Notes

Clock Relay starts as a trigger layer, not a complete job queue. That means it can schedule and observe work for many ecosystems: River, Faktory, Sidekiq, custom HTTP workers, shell scripts, and future agent/skill runners.

The current durable store is bbolt because it works inside one container with a mounted volume. YAML config can seed a fresh store, but the bundled local config starts empty; after that, schedule edits made through the UI are stored in bbolt. Redis is a natural next store for production deployments that need shared state, leases, and eventually multiple Clock Relay replicas.

For native queue targets, a successful Clock Relay run currently means the provider accepted the job. It does not mean the downstream worker finished the job. End-to-end worker tracking may become an SDK/decorator-style capability later, but workers should not receive Clock Relay metadata by default right now.

Likely next work includes richer run detail pages, failure integration tests, retries/backoff for trigger failures, run retention, authentication, Redis/shared-state support, and additional native providers such as River.
