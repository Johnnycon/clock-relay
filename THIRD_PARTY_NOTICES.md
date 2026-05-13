# Third-Party Notices

Clock Relay itself is licensed under the MIT License. This file is an
engineering-maintained index of third-party code used by the project and should
be refreshed when dependencies change.

This is not legal advice. For authoritative terms, use each dependency's
upstream license file. Current dependency versions are tracked in `go.mod`,
`go.sum`, and the example module under `examples/faktory/runner`.

## Runtime Dependencies

These dependencies are linked into the main `clock-relay` binary.

| Component | License | Notes |
| --- | --- | --- |
| `github.com/contribsys/faktory/client` | MPL-2.0 for the client package; the parent Faktory server project is AGPL-3.0 | Clock Relay uses Faktory as an integration point by importing the Go client package and submitting jobs to a user-provided Faktory server. The Clock Relay product does not embed, run, or redistribute the Faktory server. The upstream license file states that the client and worker libraries are not covered by the AGPL server license. Package scanners may still report both AGPL-3.0 and MPL-2.0 because they inspect the mixed-license module. |
| `github.com/contribsys/faktory/internal/pool` | MIT | Transitive package imported by the Faktory client. |
| `github.com/contribsys/faktory/util` | See Faktory client note | Transitive package imported by the Faktory client. Keep this on the review list because it lives in the mixed-license Faktory module. |
| `github.com/netresearch/go-cron` | MIT | Cron parsing. |
| `go.etcd.io/bbolt` | MIT | Local durable store. |
| `golang.org/x/sys` | BSD-3-Clause | Transitive dependency of bbolt. |
| `gopkg.in/yaml.v3` | MIT and Apache-2.0 | YAML config parsing. Upstream also includes a `NOTICE` file. |

The Go standard library/runtime is linked into Go binaries and is distributed
under the Go project's BSD-style license.

## Example-Only Dependencies

These dependencies are used by `examples/faktory/runner`.

| Component | License | Notes |
| --- | --- | --- |
| `github.com/contribsys/faktory_worker_go` | MPL-2.0 | Used by the example worker only. |
| `github.com/contribsys/faktory/client` | MPL-2.0 for the client package; the parent Faktory server project is AGPL-3.0 | Transitive dependency of the example worker. See the runtime Faktory note above. |
| `github.com/contribsys/faktory/internal/pool` | MIT | Transitive package imported by the Faktory client. |
| `github.com/contribsys/faktory/util` | See Faktory client note | Transitive package imported by the Faktory client. |

The Faktory example Compose file references `contribsys/faktory:latest` as a
separate service so the example can demonstrate end-to-end job submission and
worker processing. The server product is example infrastructure only; it is not
part of the main Clock Relay product path, not linked into the `clock-relay`
binary, and not copied into the Clock Relay release image. Anyone redistributing
that example environment should review the Faktory server's AGPL-3.0 terms.

## Test-Only Dependencies

These dependencies are used only while running tests for this repository or for
dependency test packages. They are not linked into the released
`clock-relay` binary unless future code starts importing them directly.

| Component | License |
| --- | --- |
| `github.com/stretchr/testify` | MIT |
| `github.com/davecgh/go-spew` | ISC |
| `github.com/pmezard/go-difflib` | BSD-3-Clause |
| `gopkg.in/check.v1` | BSD-2-Clause |
| `github.com/BurntSushi/toml` | MIT |
| `github.com/cespare/xxhash/v2` | MIT |
| `github.com/dgryski/go-rendezvous` | MIT |
| `github.com/inconshreveable/mousetrap` | Apache-2.0 |
| `github.com/redis/go-redis/v9` | BSD-2-Clause |
| `github.com/spf13/cobra` | Apache-2.0 |
| `github.com/spf13/pflag` | BSD-3-Clause |
| `go.etcd.io/gofail` | Apache-2.0 |
| `golang.org/x/sync` | BSD-3-Clause |
| `github.com/justinas/nosurf` | MIT |
| `github.com/pkg/errors` | BSD-2-Clause |

## Container Images

The production Docker image is built with `golang:1.26-alpine` and runs on
`alpine:3.22`. These base images do not change Clock Relay's source license,
but a redistributed container image includes Alpine packages and should keep
their package/license metadata or ship a generated SBOM/notice bundle.

## Practical Compliance Notes

- Keep Clock Relay's own `LICENSE` file as-is for the project MIT license.
- Preserve third-party license and notice files when vendoring, copying, or
  redistributing dependency source.
- For binary and container releases, include this notices file or a generated
  third-party notice bundle alongside release artifacts.
- If the Faktory mixed-license module is a concern for a downstream policy, get
  a legal review or replace it with a dependency whose module is not tagged as
  AGPL by scanners.
