# Stack research: Scope A (seanGSISG/machinist)

Date: 2026-09-27. Input: `questions.md` only.

**Method.** Every version and fact below comes from a page or package registry opened during this research: official docs, release notes, changelogs, npm, PyPI, the Go proxy and pkg.go.dev.
- Anything not confirmed from an opened source is marked **(unverified)**.
- Design judgements are marked **(inference)**.

**Repo baseline** (read from `go.mod` and `internal/controlplane/web/package.json`):
- Go 1.26.6.
- Go modules: creack/pty v1.1.24, pelletier/go-toml/v2 v2.4.3, spf13/cobra v1.10.2, modernc.org/sqlite v1.58.0, google/uuid v1.6.0 (indirect).
- Persistence: SQLite in WAL mode with `busy_timeout=5000`; hand-rolled migrations tracked with `PRAGMA user_version` (currently 5).
- Web UI: React 19.2.8, Vite 8.2.2, Tailwind 4.3.3.
- UI tests: `node --test` + jsdom 30. No markdown library. Polling uses `window.setInterval`.

Where the evidence was balanced, the recommendations stay with this existing stack (Go, SQLite, JS/React) rather than the generic defaults (Postgres, etc.). Changing the database is out of scope for these tickets.

---

## 1. Parsing and classifying streamed CLI output (Go)

| Option | Current version | Fit | Risks | Sources |
|---|---|---|---|---|
| `bufio.Scanner` (split by line) | stdlib, Go 1.26 | Simplest reader for text and NDJSON | The default max token is 64 KiB (`MaxScanTokenSize`). A longer line returns `ErrTooLong` and **scanning stops for good**; stream-json lines with large tool results can exceed it. `Scanner.Buffer()` only raises the cap. | https://pkg.go.dev/bufio |
| `bufio.Reader.ReadSlice` / `ReadBytes` | stdlib | Full control: read the start of a line, truncate the rest, keep going | `ErrBufferFull` handling, and the slice is only valid until the next read. More code. | https://pkg.go.dev/bufio |
| `encoding/json` (v1) `Unmarshal` per line; v2 `jsontext` | v1 is stdlib. json/v2 is an experiment in Go 1.25 (`GOEXPERIMENT=jsonv2`). Go 1.27 (released Aug 2026, 1.27.1 on 2026-09-01) makes `encoding/json` use v2 by default. | Structured streams: Claude Code `-p --output-format stream-json`, and `codex exec --json` (JSONL events `thread.started`, `turn.started`, `turn.completed` with token usage, `turn.failed`, `item.*`, `error`) | A single `json.Decoder` fails on a non-JSON banner from a wrapper launcher. Don't depend on v2 while on 1.26. | https://go.dev/doc/go1.25, https://go.dev/doc/go1.27, https://code.claude.com/docs/en/cli-reference, https://developers.openai.com/codex/noninteractive |
| `regexp` (RE2) | stdlib | Classifies free text ("usage limit", "429") in time linear in the input, so safe on long or hostile output | Patterns tied to vendor wording break silently. Keep them in a table with fixture tests. | https://pkg.go.dev/regexp |

**Wrapper detection:** no source covers this. Output shape (is the line JSON, is it a known event type) is the most robust signal; argv and env are hints only **(inference)**.

**Recommendation:**
- Read with `bufio.Reader`, capping and truncating each line.
- Call `json.Unmarshal` on lines starting with `{`, and fall back to an RE2 rule table for everything else.
- Detect wrappers by output shape.

Rationale: this survives long lines and mixed wrapper output using only the stdlib, and passes `-race`.

## 2. Stable instance identity and orphan detection

| Option | Current version | Fit | Risks | Sources |
|---|---|---|---|---|
| `/etc/machine-id` | systemd spec | Stable per host; generated at install or first boot | The man page calls it confidential: do not expose it on the network, and hash it with a keyed, application-specific hash (`sd_id128_get_machine_app_specific`; HMAC-SHA256 in Go). Linux only. Cloned images may share an ID **(unverified)**. | https://man7.org/linux/man-pages/man5/machine-id.5.html |
| UUID persisted in the worker state dir | google/uuid v1.6.0 (2024-01-23), has `NewV7` | Portable; identifies the worker instance and survives restarts; already an indirect dependency; trivial to test offline | Lost if the state dir is wiped, duplicated if the dir is cloned | https://pkg.go.dev/github.com/google/uuid |
| `pid@hostname` | n/a | This is Temporal's default worker identity; good for display | Temporal's docs note the PID is always 1 in Docker and hostnames are random on ECS, so it is not stable | https://docs.temporal.io/workers |
| Incarnation/boot counter + lease with fencing token | Linux `/proc/sys/kernel/random/boot_id`; Kubernetes Lease (`holderIdentity`, `leaseDurationSeconds`, `renewTime`) | A changed boot_id or incarnation marks earlier in-flight runs as orphaned. Fencing tokens increase on every acquire, and stale writes are rejected. | Fencing only works if the control plane checks the token on every write | https://man7.org/linux/man-pages/man4/random.4.html, https://kubernetes.io/docs/concepts/architecture/leases/, https://martin.kleppmann.com/2016/02/08/how-to-do-distributed-locking.html |

**Recommendation:**
- Instance ID: a UUID persisted in the worker state dir.
- On registration, the control plane bumps an incarnation counter in SQLite and sends it to the worker.
- Runs record the incarnation that leased them.
- When an instance registers with a new incarnation, the control plane interrupts that instance's runs from any older incarnation.

Rationale: portable and fully offline-testable, and it never exposes machine-id.

## 3. Side-effect-free GETs, list vs detail, cancel status codes

| Option / precedent | Current version | Fit | Risks | Sources |
|---|---|---|---|---|
| RFC 9110 semantics | RFC 9110 (2022) | Safe methods "MUST NOT change the resource state". 409 means a conflict with the current state. 404 means not found or not disclosed. | none | https://www.rfc-editor.org/rfc/rfc9110.html |
| GitHub Actions cancel | REST docs (current) | Cancel returns **202**, or **409** when the run can't be cancelled. Lists use `per_page` (max 100) and `page`. | 409 on a retry makes cancel non-idempotent for clients | https://docs.github.com/en/rest/actions/workflow-runs |
| Stripe cancel | API docs (current) | Returns an error if the object is already canceled or not cancelable | Same | https://docs.stripe.com/api/payment_intents/cancel |
| Google AIP-136 / AIP-158 | current | Side-effecting custom verbs must be POST. Pagination uses opaque `page_token` / `next_page_token`; oversize `page_size` is coerced down, not rejected. | Larger change than this repo needs | https://google.aip.dev/136, https://google.aip.dev/158 |
| RFC 9457 problem details | RFC 9457 (obsoletes 7807) | `application/problem+json` with `type`, `title`, `status`, `detail`, `instance` | Clients must handle JSON error bodies | https://www.rfc-editor.org/rfc/rfc9457.html |
| Go `net/http.ServeMux` patterns | Go 1.22+, current in 1.26 | `"GET /api/v1/jobs/{id}"`, `r.PathValue`. A wrong method gets 405 with `Allow`. Go 1.26 changed trailing-slash redirects to 307. | none | https://pkg.go.dev/net/http, https://go.dev/doc/go1.26 |

**Recommendation:**
- GETs only read.
- `GET /jobs` returns a slim, capped summary list; `GET /jobs/{id}` returns detail with capped run history; `GET /runs/{id}` returns run detail.
- `POST /jobs/{id}/cancel` status codes:

  | Job state | Response |
  |---|---|
  | Active | 202 (or 200 if the cancel completes synchronously) |
  | Already cancelled | 200 with the job (idempotent retry) |
  | Finished another way | 409 |
  | Unknown | 404 |

- Clients treat 404 and 409 as terminal information, not fatal errors.

Rationale: this matches RFC 9110 and the GitHub/Stripe precedent, keeps retries safe, and is a small upstreamable change.

## 4. Frontend: visibility-aware polling, safe markdown, mobile layout

### 4a. Pausing polling when the tab is hidden

| Option | Current version | Fit | Risks | Sources |
|---|---|---|---|---|
| Custom `usePolling` hook on the Page Visibility API | Baseline since 2015 | No dependency. `visibilitychange` + `document.visibilityState`; MDN names dashboard polling as a use case. Easy to test in jsdom by stubbing `visibilityState`. | You write refetch-on-return yourself | https://developer.mozilla.org/en-US/docs/Web/API/Page_Visibility_API |
| TanStack Query | 5.104.0 (2026-09-26), React ^18/^19 | `refetchIntervalInBackground` defaults to false; adds caching and dedupe | New dependency and paradigm | npm registry; TanStack query-core source |
| SWR | 2.5.1 (2026-08-12) | `refreshWhenHidden` defaults to false | New dependency | https://swr.vercel.app/docs/api |

**Recommendation:** a custom hook. It matches the existing `setInterval` code and adds no dependency.

### 4b. Rendering untrusted markdown

| Option | Current version | Fit | Risks | Sources |
|---|---|---|---|---|
| react-markdown | 10.1.0 (2025-03-07), peer react >=18 | Secure by default: no `dangerouslySetInnerHTML`, raw HTML is escaped, `defaultUrlTransform` allows only safe protocols | Last release is about 18 months old. Pulls in unified/remark. Never add `rehype-raw` for untrusted input. | https://github.com/remarkjs/react-markdown |
| marked + DOMPurify | marked 18.0.14 (2026-09-22), dompurify 3.4.16 (2026-09-23) | Small and fast | marked does not sanitize; forgetting DOMPurify is an XSS; needs `dangerouslySetInnerHTML` | https://marked.js.org/ |
| markdown-it | 15.0.2 (2026-09-11) | The default preset has `html:false` and blocks `javascript:`/`data:` links | The `commonmark` preset enables HTML; still needs `dangerouslySetInnerHTML` | npm tarball `dist/markdown-it.mjs` |

**Recommendation:** react-markdown with no `rehype-raw`. It is the only option that never touches `innerHTML`.

### 4c. Sticky action bar and grouped board on small viewports

Tailwind 4.3 provides `sticky`, `bottom-0` / `top-0`, and `h-dvh` / `min-h-dvh`. MDN's `env()` example is a sticky footer with `padding-bottom: calc(1em + env(safe-area-inset-bottom))`.
- Safe-area values need `viewport-fit=cover` **(unverified on the page opened)**.
- No built-in Tailwind safe-area utility was found **(unverified)**; use an arbitrary value, e.g. `pb-[calc(1rem+env(safe-area-inset-bottom))]`.

Sources: https://tailwindcss.com/docs/position, https://tailwindcss.com/docs/height, https://developer.mozilla.org/en-US/docs/Web/CSS/env

**Recommendation:** plain Tailwind/CSS, no dependency:
- a `sticky bottom-0` action bar with safe-area padding;
- `sticky top-0` group headers on the board;
- `min-h-dvh` for the layout.

## 5. Live log tail: worker → control plane → browser

| Option | Current version | Fit | Risks | Sources |
|---|---|---|---|---|
| Piggyback on heartbeats | n/a | No new endpoint | Ties heartbeat size and latency to log volume; a burst delays liveness. Needs a per-beat cap. **(inference)** | none |
| Separate outbound chunk upload with offsets (Buildkite model) | buildkite/agent `main` `log_streamer.go` | Chunks carry `Sequence`, `Offset` and `Size`. Outbound only, so it fits "no inbound ports". Offsets make retries idempotent and reveal gaps. Buildkite caps the total log (1 GiB by default). | More requests; the server must enforce a tail or total cap | https://raw.githubusercontent.com/buildkite/agent/main/agent/log_streamer.go |
| Push to the browser via SSE | Web platform; Go `http.NewResponseController(w).Flush()` | `id:` gives Last-Event-ID resume at a byte offset | HTTP/1.1 limits a browser to 6 connections per domain across all tabs; reverse proxies may buffer the stream | https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events/Using_server-sent_events, https://pkg.go.dev/net/http |
| WebSocket | Not in the stdlib | Two-way | New module; more than a one-way tail needs | none opened |
| Browser polls `GET .../log?offset=N` | n/a | Consistent with existing polling; pauses when the tab is hidden (Q4a) | A few seconds of latency | **(inference)** |
| (Reference) GitHub Actions | REST docs | Only finished logs, via a redirect URL that expires after 1 minute; no documented live stream | Not a model for live tails | https://docs.github.com/en/rest/actions/workflow-jobs |

**Recommendation:**
- Worker to control plane: offset-sequenced chunk POSTs, separate from heartbeats.
- The control plane keeps a capped tail (ring or truncation marker) per run.
- Browser: offset polling via the Q4a hook. Add SSE later only if latency matters.

Rationale: bounded memory, ordered and idempotent delivery, outbound-only, and every step can be faked with `httptest`.

## 6. SQLite schema: labels, supersedes, unavailable_until, usage rollups

| Option | Current version | Fit | Risks | Sources |
|---|---|---|---|---|
| modernc.org/sqlite driver | v1.59.0 (2026-09-15, bundles SQLite 3.53.4); repo is on v1.58.0 | DSN `_txlock=immediate`, `_pragma=`, `_busy_timeout` | `_txlock` applies to every `BeginTx` on that DSN, so a separate read pool may be wanted **(inference)** | https://pkg.go.dev/modernc.org/sqlite |
| `BEGIN IMMEDIATE` for supersede (cancel the old job + insert the new one) | SQLite docs | A deferred read-then-write transaction may get SQLITE_BUSY when it upgrades to a write; IMMEDIATE takes the write lock up front. WAL allows one writer. | Can still get BUSY, so keep busy_timeout | https://sqlite.org/lang_transaction.html, https://sqlite.org/wal.html |
| Labels as a JSON column + `json_each` | JSON built in since 3.38; JSONB since 3.45.0 | Simple writes | Not indexable, so filters and rollups scan | https://sqlite.org/json1.html |
| Labels as a join table `job_labels(job_id,label)` with PK and `INDEX(label,job_id)` | n/a | Indexed filter and `GROUP BY label` | Must be written in the same transaction as the job **(inference)** | none (design) |
| `ALTER TABLE ADD COLUMN` | SQLite docs | Nullable `unavailable_until INTEGER` is allowed | No PK/UNIQUE; NOT NULL needs a default; otherwise a table rebuild is needed | https://sqlite.org/lang_altertable.html |
| pressly/goose v3 / golang-migrate | goose v3.28.0 (pkg.go.dev 2026-09-02; the GitHub page showed a conflicting date); golang-migrate v4.20.1 (2026-09-09) | Mature migration tools | A second version source alongside `user_version`; modernc support in golang-migrate **(unverified)** | https://pkg.go.dev/github.com/pressly/goose/v3, https://github.com/golang-migrate/migrate/releases |
| Rollups: on read with GROUP BY, vs an aggregate table or triggers | n/a | GROUP BY over indexed usage rows is always correct | Aggregate tables drift and need backfill when a dimension (e.g. labels) is added **(inference)** | none |

**Recommendation:**
- A `job_labels` join table.
- `jobs.supersedes_job_id`, with the old job cancelled in the same IMMEDIATE transaction.
- An executor-state row with a nullable `unavailable_until`.
- Usage rows per run, rolled up on read with GROUP BY.
- New migrations continue the hand-rolled `user_version` sequence (6, 7, …).

Rationale: correct under `-race`, offline and additive. No new migration dependency at this scale.

## 7. Rewind to step N and bounded loop-back

| Option | Current version | Fit | Risks | Sources |
|---|---|---|---|---|
| Temporal Reset | current docs | Creates a new execution and copies history up to the reset point; the original is preserved | Heavy event-sourcing model | https://docs.temporal.io/workflow-execution/event |
| GitHub Actions re-run | current docs | A new `run_attempt`; old attempts stay readable via `/attempts/{n}` | Limited to 30 days and 50 re-runs | https://docs.github.com/en/rest/actions/workflow-runs |
| Argo `argo retry` / `retryStrategy` | current docs | `limit`, `retryPolicy`, `expression`, `backoff` | Retries **in place** on the same object, losing prior node state | https://argo-workflows.readthedocs.io/ (argo_retry, retries pages) |
| AWS Step Functions redrive | current docs | Resumes from the failed step, appends an `ExecutionRedriven` event, keeps a redrive count | 14-day window; not allowed after SUCCEEDED | https://docs.aws.amazon.com/step-functions/latest/dg/redrive-executions.html |

**Recommendation:** an append-only attempts model.
- Finished run rows are never mutated.
- A rewind or `goto` inserts new run rows with `attempt`, `previous_run_id` and `reason` (rewind / loop / retry).
- A persisted per-(job, edge) loop counter is checked against `max` inside the insert transaction.
- The current state of a step is its latest attempt.

Rationale: history and artifact lineage are preserved (the GitHub/Temporal style), and control flow stays in code.

## 8. Per-entry config error isolation and `config validate`

| Option | Current version | Fit | Risks | Sources |
|---|---|---|---|---|
| Prometheus | current docs | An invalid reload is not applied and the old config keeps running. `promtool check config` has `--lint-fatal`, which exits 3 on lint problems. | Whole-file granularity | https://prometheus.io/docs/prometheus/latest/configuration/configuration/, https://prometheus.io/docs/prometheus/latest/command-line/promtool/ |
| nginx `-t` / Caddy `validate` | current docs | nginx rolls back to the old config if a reload fails. `caddy validate` provisions modules without starting them; exit 1 means failed startup. | Whole-file | https://nginx.org/en/docs/switches.html, https://nginx.org/en/docs/control.html, https://caddyserver.com/docs/command-line |
| Terraform `validate -json` | current docs | Machine output: `valid`, `error_count`, `diagnostics[]` with severity, summary, detail and a file/line/column range | Exit codes not on the page **(unverified)** | https://developer.hashicorp.com/terraform/cli/commands/validate |
| go-toml/v2 errors (in repo) | v2.4.3 (2026-07-05) | `DecodeError.Position()` gives row and column. `DisallowUnknownFields` returns `StrictMissingError` listing every unknown key. | Decoding stops at the first type error, so per-entry isolation needs per-entry decoding | https://pkg.go.dev/github.com/pelletier/go-toml/v2 |

**Recommendation:**
- Decode in two phases: parse the file, then decode and validate each command or prompt entry separately.
- An invalid entry is marked unavailable with a file:line:col reason, and the service stays up. A whole-file syntax error keeps the last good config.
- `machinist config validate` (cobra) runs the same loader offline and prints human diagnostics (plus `--json` in Terraform's diagnostics shape). Exit 0 if valid, 1 on errors.

Rationale: no crash loop, and the check can be run before a human-batched deploy.

## 9. Hysteresis for CLI login status

| Option | Current version | Fit | Risks | Sources |
|---|---|---|---|---|
| Kubernetes probe thresholds | current docs | `failureThreshold` (default 3), `successThreshold`, `periodSeconds` | Pattern only | https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/ |
| HAProxy `rise` / `fall` / `fastinter` | docs 3.2 | `fastinter` is a shorter check interval used while a server is changing state. This is exactly the "quick recheck" requirement. | Pattern only | https://docs.haproxy.org/3.2/configuration.html |
| Envoy outlier detection | latest docs | Consecutive-failure ejection; one successful health check clears the counters | Pattern only | https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/upstream/outlier |
| sony/gobreaker v2 | v2.4.0 (pkg.go.dev 2026-01-01) | Closed / Open / HalfOpen with `ReadyToTrip` | Built for gating requests, not reporting status; doesn't persist state | https://pkg.go.dev/github.com/sony/gobreaker/v2 |

**Recommendation:** a small hand-rolled state machine.
- On the first soft failure, schedule a quick recheck (fastinter-style).
- The second consecutive soft failure marks the login expired; one success resets the count.
- Hard signals (an `expires_at` in the past, or an explicit logged-out/401) bypass the counter immediately.
- Inject a clock and a checker so tests run offline.

Rationale: it meets the fixed 2-failure rule exactly and needs no dependency.

## 10. Machine-readable status contract

| Option | Current version | Fit | Risks | Sources |
|---|---|---|---|---|
| GitHub Actions run model | REST docs | Separate `status` and `conclusion` enums, `run_attempt`, `html_url` deep link | none | https://docs.github.com/en/rest/actions/workflow-runs |
| Buildkite states | current docs | Explicit `blocked` / `waiting` / `canceling` / `expired` states for gates | none | https://buildkite.com/docs/pipelines/configure/defining-steps |
| Kubernetes conditions | api-conventions.md | `type`, `status` (True/False/Unknown), CamelCase `reason`, `message`, `lastTransitionTime`. Changes must be additive only. | More verbose | https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md |
| health+json draft | draft-inadarei-api-health-check-06 (2021-10-16, **expired**) | pass/warn/fail rollup | Not a live standard | https://datatracker.ietf.org/doc/html/draft-inadarei-api-health-check |

**Recommendation:** a versioned `/status` with `schema_version` and `generated_at`, containing:
- `gates_awaiting_approval[]`, `blocked_jobs[]`, `logins[]` (with `state` and `expires_at`) and `executors[]` (with `rate_limited_until`);
- on each item: a stable `id`, a closed-enum `state`, `since`, a CamelCase `reason` + `message`, and an `html_url` deep link;
- no prompt or spec text in lists.

Changes are additive only, and consumers must treat unknown enum values as unknown.

Rationale: it combines the GitHub, Buildkite and Kubernetes conventions, and fake consumers can code against it.

## 11. How agent CLIs report usage and rate limits

| Option | Current version | Fit | Risks | Sources |
|---|---|---|---|---|
| Claude Code `-p --output-format stream-json` | 2.1.283 (npm, 2026-09-25) | Documented text: `You've hit your session limit · resets 3:45pm` (also weekly, Opus and Sonnet variants; local clock time, sometimes with a timezone suffix). Documented `system/api_retry` event with `retry_delay_ms` and `error:"rate_limit"`. A `rate_limit_event` with `rate_limit_info.resetsAt` (epoch seconds) is seen in GitHub issues. | `rate_limit_event` is **undocumented**. An issue shows it spliced into another JSON line. Some runs exit 0 with an empty result. The CLI retries temporary 429s itself (since v2.1.199). | https://code.claude.com/docs/en/errors.md, https://code.claude.com/docs/en/headless.md, https://github.com/anthropics/claude-code (issues #78476, #38673, #49640; CHANGELOG.md) |
| Anthropic API | docs (current) | A 429 `rate_limit_error` carries `retry-after` in seconds; `anthropic-ratelimit-*-reset` headers are RFC 3339 | The spend-cap 429 (`enforced_spend_limit_reached`) has no `retry-after` | https://platform.claude.com/docs/en/api/rate-limits, https://platform.claude.com/docs/en/api/errors |
| OpenAI Codex CLI `codex exec --json` | 0.157.1 (npm, 2026-09-26) | `turn.completed.usage` holds input, cached_input, output and reasoning tokens. Source has `You've hit your usage limit. … Try again at {time}.`: local time as `%-I:%M %p` if today, else `%b %-d<ordinal>, %Y %-I:%M %p`, or "Try again later." when unknown. | A `rate_limits` snapshot in exec JSON is **unverified**. Some variants (credits, spend cap) give no time. | https://developers.openai.com/codex/noninteractive, https://github.com/openai/codex (codex-rs/protocol/src/error.rs) |
| Gemini CLI `-p --output-format json` | 0.61.0 (npm, 2026-09-24) | Exit codes: 0, 1 (general/API), 42 (input), 53 (turn limit); **no rate-limit exit code**. Internally classifies `TerminalQuotaError` vs `RetryableQuotaError` (the latter uses `retryDelay`) and retries up to 10 times. | Daily quota has no reset time | https://github.com/google-gemini/gemini-cli (docs/cli/headless.md, googleQuotaErrors.ts, PR #19949) |
| HTTP `Retry-After` | RFC 9110 §10.2.3 | Delay in seconds or an HTTP-date | Section text not extracted in the fetch **(unverified detail)** | https://www.rfc-editor.org/rfc/rfc9110.html |

**Recommendation:** an ordered classifier that records `reset_source` on every result.
1. Structured data first: `resetsAt`, `retry-after`, ratelimit reset headers, `retryDelay`.
2. Then the vendor regex table (Claude `resets <time>`, resolved to the next future occurrence in the host's timezone; Codex `Try again at …`).
3. Spend-cap or credit messages mean `rate_limited` with no automatic reset (operator attention).
4. **Heuristic fallback** when there is no time: capped exponential backoff (for example 60s → 30–60 min) with a probe, labelled an estimate.

Requeue as an infrastructure retry. All rules are covered by recorded-fixture tests.

Rationale: reset times are only reliable when structured, and text formats vary by plan and version.

## 12. Testing with everything faked

| Option | Current version | Fit | Risks | Sources |
|---|---|---|---|---|
| `net/http/httptest` | stdlib | `NewRecorder` for handler tests, `NewServer` for fake worker and control-plane clients | Loopback I/O doesn't work well inside synctest bubbles | https://pkg.go.dev/net/http/httptest **(page not opened; stdlib)** |
| `testing/synctest` | GA in Go 1.25 | A fake clock per bubble; well suited to leases, backoff, rechecks and reset scheduling; works with `-race` | Network and syscalls aren't durably blocking, so use in-memory fakes or `net.Pipe`. No `T.Run` or `T.Parallel` inside. | https://go.dev/doc/go1.25, https://pkg.go.dev/testing/synctest, https://go.dev/blog/testing-time |
| Golden files: hand-rolled `-update` vs gotest.tools/v3/golden | gotest.tools v3.5.2 (2024-09-05) | Snapshots of CLI stream fixtures and API JSON | The library hasn't released in about 2 years; hand-rolled is about 15 lines | proxy.golang.org |
| node:test + `mock.timers` + jsdom (current) | MockTimers stable since Node v23.1.0; jsdom 30.1.1 (2026-09-22) | Already in the `just check` gate; fakes intervals and `Date` | No render helpers; destructured timer imports aren't mocked | https://nodejs.org/api/test.html |
| Vitest | 5.0.2 (2026-09-25), peer vite ^8 OK | Integrated environment | A second runner and config; fake-timer details **(unverified)** | npm registry |
| @testing-library/react | 16.3.3 (2026-08-27), React ^19 | Query helpers; works under node:test + jsdom | Two added dependencies | npm registry |

**Recommendation:**
- Go: `httptest` for handlers; `synctest` + in-memory fakes for lease, cancel and timing logic; testdata golden fixtures with a hand-rolled `-update` flag; everything under `-race`.
- UI: stay on `node --test` + jsdom + `mock.timers`, with visibility stubbed via `document.visibilityState`.

Rationale: no new runner, and it fits the existing gate.

## 13. Splitting tickets to minimize merge conflicts

| Option | Current version | Fit | Risks | Sources |
|---|---|---|---|---|
| Per-feature files and additive-only API fields | convention | New types and handlers go in new files (`*_labels.go`, a UI module per view). Shared structs only gain optional `omitempty` fields. | Registration lists remain hot spots; keep them sorted, one entry per line **(inference)** | none |
| Changelog/doc fragments | @changesets/cli 3.0.3 (2026-09-14); towncrier 26.9.0 (2026-09-04) | One fragment file per change, so PRs don't edit the same lines | Tooling is language-specific; a plain `changes/<ticket>.md` achieves the same **(inference)** | npm and PyPI registries |
| GitHub merge queue | current docs | Tests each PR against the base plus the PRs queued ahead of it | CI must trigger on `merge_group`. **Out of scope: no `.github/workflows/` edits allowed.** | https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-a-merge-queue |
| CODEOWNERS | current docs | Routes review | Doesn't prevent conflicts; lives in `.github/` | https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-code-owners |

**Recommendation:**
- Foundation tickets go first: the schema migration for labels/supersedes/executor state, and the status contract types. Rewind and goto depend on labels/supersedes.
- Feature tickets add new Go and UI files and append optional fields.
- Each shared doc (README.md, ARCHITECTURE.md, docs index) is owned by exactly one ticket, or by a final docs ticket.
- No merge-queue or CI changes.

Rationale: a clear dependency order with minimal shared-line edits.

## 14. Surfacing "needs attention" in the dashboard

| Option | Current version | Fit | Risks | Sources |
|---|---|---|---|---|
| Server-derived read-only fields (`attention_reason` enum, `waiting_since`) | n/a | One source of truth, golden-testable, computed on read so GETs have no side effects | Small API change **(inference)** | none |
| Client-only derivation | n/a | No API change | Logic drifts between views and the external status consumer **(inference)** | none |
| `document.title` count "(N) Machinist" | web platform | Works everywhere, no permission | MDN citation not opened **(unverified)** | none |
| Badging API `navigator.setAppBadge` | MDN: limited availability, not Baseline | Optional PWA enhancement | Needs a secure context and permission; patchy support | https://developer.mozilla.org/en-US/docs/Web/API/Navigator/setAppBadge |
| WCAG 2.2 SC 1.4.1 / 1.4.11 | W3C | Status can't be conveyed by color alone; indicators need 3:1 contrast | none | https://www.w3.org/WAI/WCAG22/Understanding/use-of-color.html, https://www.w3.org/WAI/WCAG22/Understanding/non-text-contrast.html |
| `Intl.RelativeTimeFormat` | Baseline since Sept 2020 | Shows "5 min ago" ages with no library | You pick the unit yourself | https://developer.mozilla.org/en-US/docs/Web/JavaScript/Reference/Global_Objects/Intl/RelativeTimeFormat |

**Recommendation:**
- The API supplies `attention_reason`, `waiting_since` and superseded flags, derived on read.
- The UI computes counts, `document.title`, relative ages (`Intl.RelativeTimeFormat`) and filtering (hiding superseded attempts).
- Use a distinct tone with an icon and text label (not color only).

Rationale: consistent for the UI and external consumers, and no side effects.

## 15. Metrics and events for stalls, auth, cancels and usage

| Option | Current version | Fit | Risks | Sources |
|---|---|---|---|---|
| `log/slog` | stdlib since Go 1.21 | Zero-dependency structured logs for every transition | Can't be queried by the API | stdlib (no page opened) |
| Append-only SQLite `events` table + read API | modernc sqlite (existing) | Persisted, queryable per `ticket` label, testable offline | Needs retention and pruning | none **(inference)** |
| Prometheus client_golang `/metrics` | v1.24.1 (2026-07-24) | Aggregate counters and gauges | The docs warn against high-cardinality labels, which rules out a per-ticket label. New dependency. | https://prometheus.io/docs/practices/naming/, https://prometheus.io/docs/practices/instrumentation/ |
| OpenTelemetry Go | v1.46.0 (2026-08-25) | `gen_ai.usage.input_tokens` / `output_tokens` | The GenAI semantic conventions are still at **Development** status; heavy; needs a collector | proxy.golang.org, https://github.com/open-telemetry/semantic-conventions-genai |

**Recommendation:**
- The engine records an append-only events table: `ts`, `job_id`, `labels` (including `ticket`), `executor`, `model`, `kind` (rate_limit_stall start/end, auth_change, cancel, usage), `reset_at` / `reset_source`, and token counts including cached ones.
- Each event is also mirrored to slog.
- The events are exposed via a read-only API.
- External tooling derives attended/unattended rates and stall counts from that API.
- Prometheus and OpenTelemetry are deferred. Column names are chosen to map to `gen_ai.usage.*`.

Rationale: per-ticket cardinality belongs in a queryable store, not in metrics labels.

---

## Open or unverified items
- Claude Code `rate_limit_event` is undocumented (known only from issues), so treat it as best-effort with a text fallback.
- A Codex exec JSON `rate_limits` snapshot is unverified.
- Terraform validate exit codes, the exact RFC 9110 Retry-After grammar text, a Tailwind safe-area utility, and `viewport-fit=cover` were not confirmed on opened pages.
- The pressly/goose release date conflicted between pkg.go.dev and GitHub. It isn't needed, because no new migration tool is recommended.
- Go 1.27 is released and makes json/v2 the default. The repo stays on 1.26.6 per the constraints, so avoid v2-only APIs.
