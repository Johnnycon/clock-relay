# Clock Relay Faktory Example

This example runs Clock Relay against a real Faktory server and a small Go worker.

## Run Manually

```sh
docker compose up --build
```

Then open:

- Clock Relay: http://localhost:9808
- Faktory UI: http://localhost:7420
- Runner probe: http://localhost:8090/processed

The Compose project has a stable name, `clock-relay-faktory`, so container and volume names are predictable. The bundled Clock Relay config starts with no jobs, so the first screen is empty until you create one.

Create a Faktory job in Clock Relay and choose one of the runner's registered job types below. The runner starts one worker for the `default` queue with concurrency `2` and a separate worker for the `reminders` queue with concurrency `1`, so reminder jobs cannot consume default-job capacity. Faktory args are entered as a JSON array when creating or editing jobs in the Clock Relay UI; use `[]` for jobs that do not need arguments. Clock Relay records success when Faktory accepts the job, and Faktory plus the worker own job execution after enqueue.

## Runner Job Types

- `smoke_job`: fast smoke handler on the `default` queue. The smoke test creates and triggers a temporary `faktory-smoke` schedule for this job type.
- `say_hello`: simple handler on the `default` queue.
- `meal_reminder`: long-running handler on the `reminders` queue. The worker sleeps for 10 seconds before recording completion so queue isolation can be tested manually.

The runner code is split by responsibility:

- `runner/main.go`: starts the default worker, reminder worker, and probe API.
- `runner/jobs.go`: registers job names and handlers.
- `runner/recorder.go`: stores processed job details in memory for the probe API.

The run API will include structured enqueue details:

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

## Smoke Test

```sh
./smoke.sh
```

The smoke test:

- starts Faktory, Clock Relay, and the example runner with Docker Compose
- creates a temporary `faktory-smoke` schedule through the Clock Relay API
- manually triggers that schedule through Clock Relay
- waits for Clock Relay to record the Faktory JID
- waits for the runner to process that JID
- verifies that the configured Faktory args reached the worker unchanged

The smoke test only covers the temporary `faktory-smoke` schedule. The slower `meal_reminder` handler is intentionally for human testing and is not part of the repeatable smoke script.

## Manual Testing Notes

Useful checks while developing the Faktory integration:

- Create and trigger a `meal_reminder` job on `reminders`, then trigger `smoke_job` or `say_hello` on `default`; default-queue jobs should still process while the reminder worker is sleeping.
- Open the Faktory UI and confirm jobs land on the expected queues.
- Open `/processed` on the runner probe to inspect processed job type, args, JID, and timestamp.
- Edit a Faktory job in Clock Relay and confirm JSON array args round-trip without being flattened into strings. Empty args should remain `[]`.

To reset the example data:

```sh
docker compose down -v
```
