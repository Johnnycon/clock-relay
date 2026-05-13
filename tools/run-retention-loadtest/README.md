# Run Retention Load Test

Manual load-test helper for Clock Relay run-history retention. This is not part of
the Clock Relay runtime path.

Examples:

```sh
go run ./tools/run-retention-loadtest --mode indexed --scenario cleanup --runs 100000 --retain 10000
go run ./tools/run-retention-loadtest --mode scan-sort --scenario cleanup --runs 100000 --retain 10000
go run ./tools/run-retention-loadtest --mode indexed --scenario steady --runs 1000000 --steady-insert 100
go run ./tools/run-retention-loadtest --mode scan-sort --scenario steady --runs 1000000 --steady-insert 100
```

`cleanup` measures a large prune from `--runs` down to `--retain`.
`steady` measures the common long-lived case: retain `--runs`, insert
`--steady-insert` newer runs, then prune back to `--runs`.
