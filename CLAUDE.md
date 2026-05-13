# Clock Relay Developer Context

## Project Intent

Clock Relay is an open source, self-hosted infrastructure product for scheduled jobs and simple job triggering. The long-term direction is to cover the common scheduling and job-runner needs of self-hosted applications running on Docker Compose, Docker Swarm, Kamal, DigitalOcean, Hetzner, and similar small-infra environments.

The product should start small and composable. Clock Relay owns timing, persisted job definitions, run history, visibility, and trigger delivery. Other systems can own the actual work: application HTTP endpoints, shell scripts, River, Faktory, Sidekiq, Redis-backed workers, future agent/skill runners, etc.

This project is still early. Treat the current architecture as a working prototype, not as a fixed design. If new requirements, provider integrations, testing needs, or implementation details reveal a better architecture, propose it clearly instead of forcing new work through old assumptions. The goal is a robust Clock Relay product that can evolve with real usage, not strict preservation of the first implementation.

Current product language:

- **Job**: the saved user-created unit of work.
- **Run**: one execution/trigger attempt of a job.
- **Schedule**: the timing part of a job: `once`, `rate`, or `cron`.
- **Target**: how Clock Relay triggers the job, currently `http` or `faktory`.

Avoid user-facing terms like "descriptor"; it is too implementation-oriented.

## Current Implementation

This is a Go service with one binary:

```sh
go run ./cmd/clock-relay --config clock-relay.example.yaml
```

The app listens on `:9808` by default and serves both JSON APIs and a minimal server-rendered UI.

Important files:

- `cmd/clock-relay/main.go`: CLI entrypoint, config loading, store setup, server lifecycle.
- `internal/config`: YAML config, schedule/target structs, Faktory instance config, validation, defaults.
- `internal/model`: run model/statuses.
- `internal/store`: store interface, memory store, bbolt store, retention pruning.
- `internal/target`: HTTP and native Faktory target implementations.
- `internal/engine`: schedule registration, runtime job CRUD, manual triggers, run execution.
- `internal/server`: API routes and embedded HTML/CSS/JS UI templates.
- `clock-relay.example.yaml`: local/dev config with no seed jobs.
- `Dockerfile` and `compose.yaml`: containerized local run.
- `.dockerignore`: keeps local metadata and generated state out of release build contexts.
- `.github/workflows/container.yml`: test, build, and GHCR image publishing workflow.
- `examples/faktory`: real Faktory server, runner, and smoke test.

Current implementation status:

- The main app can run from YAML or Docker Compose.
- Jobs can be created, edited, deleted, paused/resumed, manually run, and viewed in the UI.
- The UI supports `http` and `faktory` targets.
- The UI supports `once`, `rate`, and `cron` schedules.
- Faktory connections are configured as named instances with `password_env` for production secrets.
- New Faktory jobs are created through a multi-step wizard with queue autocomplete from the server.
- New HTTP job creation is not yet available in the wizard UI ("Coming soon"); existing HTTP jobs can still be edited via the legacy form.
- Jobs can be paused and resumed. Pausing removes the job from the cron scheduler and persists `paused: true`; resuming re-registers it. Paused jobs cannot be manually triggered. The UI shows a "paused" badge, dims the row, and hides the Run button.
- The index page lightly polls and replaces the Jobs and Run Log sections every few seconds while visible.
- Timestamps are formatted client-side with a saved "My timezone" setting and an Auto/12-hour/24-hour format selector.
- Run results are stored only in `structured_output`.
- Process logs are written to stdout/stderr through Go `slog`; Clock Relay does not create its own application log file.
- Run lifecycle stdout events are controlled by `run_logging.stdout`: `summary` by default, `off` to disable, or `full` to include complete `structured_output`. `full` may emit sensitive or large raw target output.
- Persisted run history is bounded by `run_retention` using max records and max age in days. Completed runs are pruned on startup and periodically after finalized runs; running runs are preserved.
- Faktory can be exercised against a real server and runner through `examples/faktory`.
- River has not been implemented yet, but it is expected to be a key native-provider integration.

## Storage Model

The current durable store is bbolt. By default local data is written to:

```text
./data/clock-relay.db
```

The bbolt store has separate buckets for:

- `schedules`: persisted job definitions.
- `runs`: run history.

On first boot, if the store has no schedules, Clock Relay can seed schedules from YAML config. The bundled local configs intentionally start empty, so a fresh UI has no jobs. After UI/API edits are persisted in bbolt, bbolt becomes the source of truth.

There is also an in-memory store for tests and ephemeral development.

Jobs are intentionally unbounded. Run history should not be unbounded: the config defaults to keeping at most 10,000 completed run records and 30 days of completed history. The bbolt store maintains run indexes for retention, recent-run reads, and running-run checks, and deletes old records from the `runs` bucket. bbolt may not immediately shrink the database file on disk after deletion; strict physical compaction would need a separate maintenance step. Clearing the run log deletes all run records.

Redis is still a likely future production store, especially when adding multiple Clock Relay replicas, leases, shared state, or stronger production deployment guidance.

## Scheduler Behavior

Clock Relay uses three schedule types instead of forcing every job through cron. The goal is to make simple intervals and one-time jobs obvious while reserving cron for calendar-based recurrence.

Clock Relay now stores schedule intent explicitly with `schedule_type`: `once`, `rate`, or `cron`. Existing persisted schedules without `schedule_type` are treated as `cron` for backward compatibility.

Timezone selection in the UI uses a curated IANA timezone dropdown. The binary imports Go `time/tzdata` so static/container builds can validate and load timezones reliably.

Current scheduler model:

- Store the user's scheduling intent directly. Do not infer schedule type by reverse-engineering a cron expression.
- The form presents schedule type directly as `rate`, `once`, or `cron`. Each type shows only its relevant fields; users should not see cron syntax unless they choose cron.
- Keep `timezone` as a first-class IANA timezone field for every schedule type. User-authored schedule times should be modeled as local wall-clock datetime plus timezone, not as offset-only timestamps.
- There is not yet a persisted server-side global timezone default. The UI has a client-side "My timezone" setting saved in `localStorage`; it defaults to the browser IANA timezone with `Intl.DateTimeFormat().resolvedOptions().timeZone` and is used to default new schedules. New-job `datetime-local` values must be generated in that selected IANA timezone, not the browser/system timezone. The selected schedule timezone is still stored on each job. Config/API-created jobs still default to `UTC` unless a timezone is supplied.
- User-facing UI timestamps should be shown in the user's selected timezone. Show schedule timezone context when the job's configured timezone differs from the user's selected timezone. Avoid making operators mentally convert from UTC.
- `once`: use `run_at` plus `timezone`. One-time schedules fire at most once and need durable completion state such as `completed_at`; completion should not be inferred only from in-memory scheduler state. After Clock Relay creates the scheduled run attempt, it persists `completed_at` and leaves the job visible with no upcoming run. Editing a completed one-time job keeps `completed_at` only if both `run_at` and `timezone` are unchanged; changing either field makes the job schedulable again.
- If Clock Relay starts and finds an uncompleted one-time schedule whose `run_at` is already in the past, it currently triggers promptly. A more explicit missed-run policy may be added later.
- `rate`: use `starts_at`, `timezone`, `rate_interval`, and `rate_unit`. Rate schedules are fixed elapsed intervals from the persisted anchor: `starts_at + N * interval`. Rate is the default for simple repeating intervals.
- For rate schedules, `rate_unit: days` means a fixed 24-hour duration, not "same local wall-clock time every calendar day." Users who want daily-at-local-time behavior should use the calendar schedule path, currently cron.
- `cron`: use five-field `cron` plus `timezone`. Cron schedules are calendar-matching schedules evaluated in that timezone and do not need `starts_at` for their core recurrence meaning.
- Optional future `starts_at` / `ends_at` fields can be shared activation windows, but they should not change cron's recurrence semantics.

Example persisted schedule shapes:

```yaml
schedules:
  - name: one-time-maintenance
    schedule_type: once
    run_at: "2026-05-10T09:00"
    timezone: America/Chicago
    completed_at: null

  - name: heartbeat
    schedule_type: rate
    starts_at: "2026-05-08T10:30"
    timezone: America/Chicago
    rate_interval: 15
    rate_unit: minutes

  - name: weekday-report
    schedule_type: cron
    cron: "0 9 * * 1-5"
    timezone: America/Chicago
```

Current scheduler implementation:

- `github.com/netresearch/go-cron` is pinned in `go.mod` and used directly in `internal/engine`.
- Cron parsing uses five-field cron plus descriptors. Use `cron.DowOrDom` so Clock Relay preserves normal/POSIX-style day-of-month OR day-of-week behavior rather than netresearch/go-cron's default AND behavior.
- Schedule timezones are encoded into cron specs with `CRON_TZ=...`.
- `rateSchedule` and `onceSchedule` implement the cron package's `Schedule` interface directly with `Next(time.Time) time.Time`; do not force these schedule types through generated cron specs.
- Prefer isolating the underlying scheduler library behind a small internal adapter so Clock Relay's persisted model is not coupled to a specific cron package.

Cron library direction:

- Treat netresearch/go-cron as useful but early-adopter risk. It has relevant features for Clock Relay's roadmap (`FakeClock`, `ValidateSpec`, `NextN`, pause/resume, triggered jobs, retry wrappers), but it is still v0.x and should stay behind an adapter before scheduler behavior spreads further through the codebase.
- The migration from robfig/cron is complete. Avoid reintroducing robfig-specific assumptions, especially around DOM/DOW behavior, timezone parsing, validation, and runtime entry management.

Concurrency policy currently supports:

- `forbid`: skip a new run if that job has a running run.
- `allow`: permit overlapping runs.

Day-one multi-replica safety is intentionally not implemented yet.

## Targets

Supported target types:

- `http`: sends a request to an app/worker endpoint. By default the body includes run metadata unless a body is configured in code/config.
- `faktory`: enqueues a native Faktory job with `job_type`, `queue`, and arbitrary `args`. The worker is not given Clock Relay metadata by default; Clock Relay records the Faktory JID in run `structured_output`.

HTTP targets are useful for app-owned job enqueueing and generic webhook-style integrations.

### Faktory Instances

Faktory connections are configured as named instances in the top-level `faktory` config section. Each instance defines a connection URL and an optional password. Faktory jobs reference an instance by name rather than embedding connection details per-job.

```yaml
faktory:
  - name: default
    url: tcp://faktory:7419
  - name: analytics
    url: tcp://analytics-faktory:7419
    password_env: ANALYTICS_FAKTORY_PASSWORD
```

Instance names appear as a dropdown in the UI when creating or editing a Faktory job. The job's `target.instance` field stores the selected instance name.

`password_env` names the environment variable that holds the Faktory password for that instance. If omitted, no password is used (typical for local/dev). Do not embed passwords in Faktory URLs; Clock Relay reads Faktory passwords from `password_env`. For production:

```yaml
faktory:
  - name: production
    url: tcp://faktory:7419
    password_env: FAKTORY_PASSWORD
```

```env
FAKTORY_PASSWORD=your-production-secret
```

Use lower `snake_case` for example Faktory `job_type` values. Faktory accepts arbitrary strings, but the examples should stay consistent across YAML config, UI docs, and runner registration.

Faktory `args` are always a JSON array in the UI. `[]` is valid for workers that do not need arguments; Clock Relay normalizes nil args to an empty slice before enqueueing because Faktory requires an `args` field.

Native provider targets are expected to become important as the product matures, especially for systems like Faktory and River. Do not assume those integrations must fit the current `TriggerTarget(ctx, schedule, run, faktoryInstances)` helper forever. Native queue targets may need provider-specific config, validation, connection/pool lifecycle, structured run output, local example runners, and integration tests against real provider services.

For native queue integrations, be explicit about the boundary Clock Relay owns. A successful Clock Relay run may mean "the external provider accepted the job" rather than "the external worker completed the job", unless the product intentionally adds provider-side completion tracking.

Do not inject Clock Relay metadata into native provider jobs by default. The current Faktory target sends only the configured worker args. If customers later need end-to-end tracking, the likely shape is an optional SDK/decorator/wrapper that can report lifecycle events consistently, not hand-written metadata handling in every worker.

## Run Output Model

Runs use `structured_output` as the canonical target result field. Do not add a parallel `output`, `raw_output`, or `target_metadata` field.

Use `structured_output.raw` for unstructured target text:

- `http`: response body goes in `structured_output.raw`; structured details like `status_code` can live beside it.
- `faktory`: enqueue details live as structured fields like `provider`, `jid`, `queue`, and `job_type`; include `raw` as a simple display summary for now.

The UI currently displays `structured_output.raw` in the run log when present. That is a display choice, not a reason to reintroduce a separate top-level output field.

The index UI currently uses simple polling rather than websockets or SSE. It fetches the rendered index page every few seconds while the tab is visible and replaces only the Jobs and Run Log sections. Keep this lightweight unless real multi-viewer needs emerge.

## Faktory Example

The Faktory example is the current real-provider playground:

```sh
cd examples/faktory
docker compose up --build
```

It uses a stable Compose project name in `examples/faktory/compose.yaml`:

```yaml
name: clock-relay-faktory
```

Ports:

- Clock Relay: `http://localhost:9808`
- Faktory UI: `http://localhost:7420`
- Runner probe API: `http://localhost:8090/processed`

Runner job types:

- `smoke_job`: fast handler on the `default` queue. The smoke test creates a temporary `faktory-smoke` schedule for this job type through the API.
- `say_hello`: simple handler on the `default` queue.
- `meal_reminder`: long-running handler on the `reminders` queue that sleeps for 10 seconds in the worker.

The runner is intentionally shaped like a real Faktory worker project:

- `examples/faktory/runner/main.go`: process lifecycle, two worker managers, probe HTTP server.
- `examples/faktory/runner/jobs.go`: job registration and handlers.
- `examples/faktory/runner/recorder.go`: in-memory record of processed jobs for manual inspection and smoke tests.

The runner starts one worker manager for `default` with concurrency `2` and another for `reminders` with concurrency `1`. This lets manual testing verify that slower reminder jobs do not consume default-job capacity.

Run the repeatable Faktory smoke test with:

```sh
cd examples/faktory
./smoke.sh
```

The smoke test is intentionally narrow: it creates and triggers only a temporary `faktory-smoke` schedule, waits for the Faktory JID in `structured_output.jid`, then verifies the example worker received the configured args. Do not add the long-running `meal_reminder` job type to the smoke test unless the test strategy changes.

## API Surface

Current routes:

- `GET /healthz`
- `GET /v1/schedules`
- `POST /v1/schedules`
- `POST /v1/schedules/{name}/run`
- `POST /v1/schedules/{name}/pause`: toggles pause state; returns `{"status": "paused"|"resumed", "paused": "true"|"false"}`.
- `POST /v1/schedules/{name}/delete`
- `GET /v1/runs?limit=100`
- `POST /v1/runs/clear`
- `GET /v1/faktory/queues?instance={name}`: introspects a named Faktory instance and returns its queue names as a JSON array. Used by the wizard UI to populate queue suggestions.

Current UI routes:

- `/`: jobs list and run log. Supports `?job={name}` to filter the run log to a specific job.
- `/schedules/new`: job type picker (Faktory, Web request).
- `/schedules/new?type=faktory`: multi-step Faktory job wizard.
- `/schedules/{name}/edit`: edit job.

The API still uses `schedules` in route names because the first prototype started from scheduler semantics. Product copy should say "Job"; route renaming can be considered later when versioning/API stability is addressed.

## UI Direction

The UI matters for shaping the product. Keep it simple, operational, and scannable.

### Job Creation Wizard

New jobs are created through a type-specific wizard flow. The user first picks the job type, then the wizard walks through focused steps with a compound narrative that builds as the user progresses.

The Faktory wizard flow:

1. **Instance** (skipped if only one configured): pick the Faktory server.
2. **Job Identity**: job type (the Faktory worker job type), queue (with autocomplete from the server via `GET /v1/faktory/queues`), optional description, optional JSON args. The Clock Relay schedule name is auto-derived from the job type (`nightly_sync_job` → `nightly-sync-job`).
3. **Schedule Type**: Rate, Once, or Cron cards.
4. **Schedule Details**: only the fields for the chosen type, plus timezone (defaulted from the user's saved "My timezone" setting).
5. **Review**: summary table with a "Create job" button.

Faktory jobs use smart defaults: 5s timeout (enqueue-only), concurrency policy `allow` (runs are too fast to overlap). These are hidden from the user.

The Web request wizard is not yet implemented (shown as "Coming soon" on the type picker).

Editing existing jobs uses the same wizard template for Faktory, and the legacy form for HTTP.

### Time Display

The index page renders timestamps as `<span class="local-time" data-time="..." data-source-tz="...">` elements. Client-side JS formats them with the user's saved "My timezone" setting, defaulting to the browser timezone. The header exposes "My timezone" and Auto/12-hour/24-hour format controls. Schedule timezone context is rendered only when the configured schedule timezone differs from the user's selected timezone.

### Index Page

The overview is focused on:

- Jobs
- Run Log

The index page polls every 3 seconds and replaces the Jobs and Run Log sections. After each refresh, timestamps are re-formatted client-side. Keep this lightweight unless real multi-viewer needs emerge.

The run log shows the last 100 runs and supports a server-side job filter via a dropdown in the Run Log header. The polling URL includes the active filter so filtered views stay consistent across refreshes.

Job row actions use a two-tier layout: Pause/Resume and Run are direct text buttons for primary operational actions. Edit and Delete are grouped in a `...` overflow menu to reduce visual noise.

## Local Development

Run locally:

```sh
go run ./cmd/clock-relay --config clock-relay.example.yaml
```

Run tests:

```sh
go test ./...
```

Build:

```sh
go build ./cmd/clock-relay
```

Run with Docker Compose:

```sh
docker compose up --build
```

The compose setup persists `/app/data` in a Docker volume.
The app logs to stdout/stderr and does not create its own application log file. Keep future process logging container-native unless there is a deliberate operational reason to add a file sink.

## Container Releases

Release images are published to the GitHub Container Registry:

```text
ghcr.io/johnnycon/clock-relay:<version>
```

Use exact version tags from downstream Docker Compose, Docker Swarm, Kamal, or other deployment projects, for example `ghcr.io/johnnycon/clock-relay:0.0.1`. The `latest` tag exists for quick local trials, but do not rely on it for repeatable deploys or rollbackable infrastructure.

Version tags are created from git tags with a leading `v`:

```sh
git tag v0.0.1
git push origin v0.0.1
```

The container image tag drops the leading `v`, so `v0.0.1` publishes `ghcr.io/johnnycon/clock-relay:0.0.1`.

The canonical discovery surfaces are:

- README container section
- GitHub Releases: https://github.com/Johnnycon/clock-relay/releases
- GHCR package: https://github.com/Johnnycon/clock-relay/pkgs/container/clock-relay
- Git tags: https://github.com/Johnnycon/clock-relay/tags

For agents and automation, derive the pinned image from the newest semantic version tag:

```sh
latest_tag="$(git ls-remote --tags --refs --sort='v:refname' https://github.com/Johnnycon/clock-relay.git 'v*.*.*' | awk -F/ 'END {print $NF}')"
image="ghcr.io/johnnycon/clock-relay:${latest_tag#v}"
printf '%s\n' "$image"
```

The GitHub Actions container workflow tests the repo, builds `linux/amd64` and `linux/arm64` images, pushes on tags matching `v*.*.*`, and creates or updates the matching GitHub Release with the exact pull command. Pull requests and `main` pushes build without publishing.

Downstream Docker hosts can pull the image directly. For Swarm, deploy with `docker stack deploy -c compose.yaml clock-relay`.

The binary supports:

```sh
clock-relay --version
```

GitHub Actions injects version, commit, and build date into release builds with Go linker flags. Keep Docker labels and binary version output aligned when changing release metadata.

## Design Constraints and Next Likely Work

Keep the v0 focused: Clock Relay is a scheduler and trigger layer, not a full queue yet. This is a product boundary, not a ban on architecture changes. If a better target abstraction, config model, store boundary, or engine lifecycle model is needed to support real integrations cleanly, propose and implement it in focused steps.

Known technical debt to keep in mind before production hardening:

- `allow_concurrent_runs: false` is currently a best-effort check. The check for an existing running run and the creation of the next run are separate store calls, so concurrent triggers can race. This should become an atomic store-level claim operation when the run model settles.
- Run execution currently uses background goroutines owned by the engine. A future engine lifecycle pass should add cancellable run contexts and wait for active runs during shutdown.
- Clearing the run log is primarily a testing/development affordance. If a run is in flight, it may write a final run record after clearing. That is acceptable for now.
- Native target clients are currently simple and opened per trigger where appropriate. Revisit client lifecycle/pooling when real provider usage creates pressure for it.
- `ScheduleConfig` is currently both config and persisted job definition. That is acceptable for prototype speed, but a future migration may split API/config/persisted models.

Likely next steps:

- Add richer Faktory run/detail UI so users can inspect provider output beyond the raw run-log summary.
- Add at least one failure integration test for Faktory/provider enqueue behavior.
- Improve the manual playbook as new human-testing scenarios emerge.
- Revisit target abstraction and client lifecycle after the UI and run model expose more pressure.
- Add River as the next native provider with a real example runner/project.
- Add Redis store behind the existing `Store` interface.
- Job pause/resume is implemented. Consider whether a separate "disabled" state is needed beyond pause.
- Add retries/backoff for failed triggers.
- Add richer run detail pages.
- Add authentication before treating this as deployable beyond trusted networks.
- Add import/export or config sync for jobs.
