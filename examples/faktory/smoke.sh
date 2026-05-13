#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

project="${COMPOSE_PROJECT_NAME:-clock-relay-faktory}"

cleanup() {
  docker compose -p "$project" down -v >/dev/null
}
trap cleanup EXIT

docker compose -p "$project" up -d --build

wait_for_http() {
  local url="$1"
  local name="$2"
  local deadline=$((SECONDS + 90))
  until curl -fsS "$url" >/dev/null; do
    if (( SECONDS >= deadline )); then
      echo "timed out waiting for $name at $url" >&2
      docker compose -p "$project" logs >&2
      exit 1
    fi
    sleep 1
  done
}

wait_for_http "http://localhost:9808/healthz" "clock-relay"
wait_for_http "http://localhost:8090/healthz" "faktory runner"

create_smoke_job() {
  curl -fsS -X POST "http://localhost:9808/v1/schedules" \
    -H "Content-Type: application/json" \
    --data @- >/dev/null <<'JSON'
{
  "schedule": {
    "name": "faktory-smoke",
    "description": "Enqueues a native Faktory job for the example runner.",
    "schedule_type": "cron",
    "cron": "0 0 1 1 *",
    "timezone": "UTC",
    "timeout": "10s",
    "concurrency_policy": "forbid",
    "target": {
      "type": "faktory",
      "instance": "default",
      "queue": "default",
      "job_type": "smoke_job",
      "args": [
        {"account_id": "acct_smoke"},
        {"source": "clock-relay"}
      ]
    }
  }
}
JSON
}

trigger_job() {
  local schedule="$1"
  curl -fsS -X POST "http://localhost:9808/v1/schedules/$schedule/run"
}

run_id_from_json() {
  RUN_JSON="$1" python3 - <<'PY'
import json
import os

print(json.loads(os.environ["RUN_JSON"])["id"])
PY
}

wait_for_jid() {
  local run_id="$1"
  local deadline=$((SECONDS + 90))
  local jid=""
  while [[ -z "$jid" ]]; do
    runs_json="$(curl -fsS "http://localhost:9808/v1/runs?limit=10")"
    jid="$(RUNS_JSON="$runs_json" RUN_ID="$run_id" python3 - <<'PY'
import json
import os

runs = json.loads(os.environ["RUNS_JSON"])
run_id = os.environ["RUN_ID"]
for run in runs:
    if run.get("id") != run_id:
        continue
    if run.get("status") == "failed":
        raise SystemExit(run.get("error", "run failed"))
    jid = run.get("structured_output", {}).get("jid")
    if jid:
        print(jid)
PY
)"
    if [[ -n "$jid" ]]; then
      echo "$jid"
      return 0
    fi
    if (( SECONDS >= deadline )); then
      echo "timed out waiting for Clock Relay to record Faktory JID" >&2
      docker compose -p "$project" logs >&2
      exit 1
    fi
    sleep 1
  done
}

wait_for_processed() {
  local jid="$1"
  local deadline=$((SECONDS + 90))
  until curl -fsS "http://localhost:8090/processed/$jid" >/dev/null 2>&1; do
    if (( SECONDS >= deadline )); then
      echo "timed out waiting for runner to process Faktory job $jid" >&2
      docker compose -p "$project" logs >&2
      exit 1
    fi
    sleep 1
  done
  curl -fsS "http://localhost:8090/processed/$jid"
}

create_smoke_job
run_json="$(trigger_job "faktory-smoke")"
run_id="$(run_id_from_json "$run_json")"
jid="$(wait_for_jid "$run_id")"
processed_json="$(wait_for_processed "$jid")"
PROCESSED_JSON="$processed_json" python3 - <<'PY'
import json
import os

job = json.loads(os.environ["PROCESSED_JSON"])
if job["job_type"] != "smoke_job":
    raise SystemExit(f"unexpected job_type: {job['job_type']}")
args = job["args"]
if args[0].get("account_id") != "acct_smoke":
    raise SystemExit(f"unexpected first arg: {args[0]!r}")
if args[1].get("source") != "clock-relay":
    raise SystemExit(f"unexpected second arg: {args[1]!r}")
PY

echo "ok faktory jid=$jid"
