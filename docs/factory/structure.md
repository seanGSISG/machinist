# Structure: FAC-06 Scope A (seanGSISG/machinist)

Input: design.md (approved, gate 1). D-numbers refer to its §3. Paths are relative to the repo root. `CP` =
`internal/controlplane`, `WEB` = `internal/controlplane/web/src`, `api` = `/api/v1`.
Checkpoints are offline and run under `-race`. Every slice also has to pass `just check` before merge.

## 1. Hub files (owned by S0; later slices only append one line or fill a stub)

| Hub | Why it is a hub | S0 does |
|---|---|---|
| `CP/store.go` (migration list) | every schema change | lands **migration 6** with all Scope A tables and columns (below); E8 alone gets migration 7 in S19 |
| `CP/server.go` (route table, `status`) | every endpoint | adds `registerFAC06Routes(mux)` in new `CP/routes_fac06.go`; each route starts as a 501 stub whose handler lives in a per-feature file |
| `internal/protocol/protocol.go` | worker⇄CP wire structs | adds all optional `omitempty` fields (below) in one go |
| `CP/web/src/routes.js`, `main.jsx` | UI routing and nav | reserves routes `/runs/:id`, `/jobs/:id/gate`, `/usage` with placeholder views |
| `WEB/status-loader.js` | every view's data | wrapped by `usePolling` in S3; later slices read new fields and don't edit the loader |
| `internal/cli/root.go` | CLI tree | S6 adds `config` group; S12 adds submit flags (the only two edits) |
| `CP/web/package.json` | deps | only S15 edits it (react-markdown) |

**Migration 6 contents** (all additive, nullable, or new tables; D12–D16, D22, D27):
`job_labels(job_id, label_key, value, PK(job_id,label_key))` with index on `(label_key,value)`;
`jobs.supersedes_job_id`; `runs.failure_class`, `runs.reset_at`, `runs.reset_source`, `runs.incarnation`;
`workers.incarnation`; `executor_state(worker, executor, unavailable_until, backoff_seconds, reset_source, updated_at, PK(worker,executor))`;
`run_usage(run_id PK, model, input_tokens, output_tokens, cached_input_tokens, reasoning_tokens, created_at)`;
`events(id, type, subject_kind, subject_id, cause, payload_json, created_at)` with index on `(type, created_at)`;
`run_log_tail(run_id PK, next_offset, data BLOB, truncated, updated_at)`.

**Protocol fields** (`omitempty`): `Completion{FailureClass, ResetAt, ResetSource, Usage *Usage}`,
`Usage{Model, InputTokens, OutputTokens, CachedInputTokens, ReasoningTokens}`, `PollRequest{InstanceID, Incarnation}`,
`Lease{Incarnation}`, `LogChunk{Offset int64, Data []byte, Truncated bool}`, `SubmitRequest{Labels map[string]string, SupersedesJobID}`.

## 2. Slices (in build order)

Each slice lists its **contracts**, the **files it owns**, its **checkpoint** and what it **depends on**.

**S0 Foundation / hubs.** Contracts: migration 6, protocol fields, route stubs, `store.AppendEvent(ctx, tx, Event) error`.
Owns: the hub rows above, `CP/routes_fac06.go`, `CP/events.go`, `CP/migration6_test.go`.
Check: `go test -race ./internal/controlplane -run 'TestMigration6|TestUserVersion'` (user_version=6, a v5 DB upgrades, and every S-route stub answers 501). Deps: none.

**S1 Side-effect-free GET + retention (D7, D27).** Delete the `maintainState` call from `status` (the scheduler loop at `server.go:247` already runs it) and add `pruneRetention(ctx, now)` to that loop. Config: `[control_plane] retention_days = 90`.
Owns: `CP/retention.go`, `CP/retention_test.go`.
Check: `go test -race ./internal/controlplane -run 'TestStatusIsReadOnly|TestRetentionPrune'` (a GET with an expired lease leaves it untouched; synctest clock prunes rows older than 90d). Deps: S0.

**S2 Slim list / detail read API (D8, D10 base, D25, D15 read).**
`GET api/status` → the v-schema below (S2 lands the envelope, `jobs[]` slim with finished capped at `[control_plane] finished_limit=50`, and all four new arrays present, possibly empty);
`GET api/jobs?state=&label=k:v&limit=` (slim); `GET api/jobs/{id}` (detail + ≤20 runs); `GET api/runs/{id}` (result, events, rendered_prompt);
`GET api/events?type=&since=&limit=`. Detail routes are wrapped in `authorizeArtifact`.

*Contract (Scope B dispatcher parks tickets from this):* every run entry in `GET api/jobs/{id}` `runs[]` and the
body of `GET api/runs/{id}` always carries `failure_class` (string|null, e.g. `"rate_limited"`), `reset_at`
(RFC 3339|null) and `reset_source` (`structured|regex|estimate`|null). The keys are always present; values are
`null` until S10 fills them from the migration-6 columns.

*Contract — `/status` v-schema (D10). Scope B mirrors this field for field; JSON names are exact; changes are additive only:*
```
{ schema_version: int, generated_at: RFC3339,
  jobs: [ …slim job summary… ],
  connections: [ …unchanged from MCH-05… ],
  executors: [ { id, worker, executor, state: "ok"|"rate_limited", since: RFC3339, reason, message,
                 rate_limited_until: RFC3339|null, reset_source: "structured"|"regex"|"estimate"|null } ],
  gates_awaiting_approval: [ Item ], blocked_jobs: [ Item ], logins: [ Item ],
  …existing top-level fields unchanged (e.g. invalid_commands from S6) }
Item = { id, state, since: RFC3339, reason (CamelCase), message, html_url }
```
S2 emits the envelope with `executors`, `gates_awaiting_approval`, `blocked_jobs`, `logins` as `[]`; S11 fills
`executors[]`, S14 fills the three `Item` arrays. None of them may rename, drop or retype a field.
Owns: `CP/read_api.go`, `CP/read_api_test.go`, `CP/job_summary.go`, `CP/status_schema.go` (the Go structs for the block above).
Check: `go test -race ./internal/controlplane -run 'TestStatusSlim|TestStatusSchemaGolden|TestJobDetail|TestRunDetail|TestDetailAuth|TestEventsAPI'` (no prompt or spec in list bodies; a cross-origin GET without a token gets 403; a golden JSON asserts every v-schema key and every run's `failure_class`/`reset_at`/`reset_source` keys are present as `null`). Deps: S0, S1.

**S3 usePolling + UI on the slim API (D17).** `usePolling(fetcher, intervalMs) → {data, error, refresh}`: pauses on `visibilitychange` and refetches when the tab is visible again. Task detail loads `GET /jobs/{id}` on open. Replaces both `setInterval`s in `connections.jsx`.
Owns: `WEB/use-polling.js`, `WEB/use-polling.test.js`; edits `status-loader.js`, `task-detail.jsx`, `connections.jsx`.
Check: `cd CP/web && node --test src/use-polling.test.js src/status-ui.test.js src/connections.test.js` (mock.timers: 0 fetches while hidden, 1 on show). Deps: S2.

**S4 Cancel semantics (D9).** `POST api/jobs/{id}/cancel`: 202 active, 200 already cancelled, 409 finished otherwise, 404 unknown. Direct jobs go to `store.CancelDirectJob` rather than `WorkflowAction`. The worker treats 404/409 from cancel and heartbeat as terminal and stops the run cleanly. Appends a `cancel` event.
Owns: `CP/cancel.go`, `CP/cancel_test.go`; edits `managedworker/worker.go` (response handling only), `managedworker/worker_test.go`.
Check: `go test -race ./internal/controlplane ./internal/managedworker -run 'TestCancel|TestWorkerTerminalOn404'`. Deps: S0.

**S5 Stable worker ID + incarnation (D6).** A UUID persisted at `<worker state dir>/instance_id`. Poll sends it. The CP bumps `workers.incarnation` on the first poll of a new process, stamps `runs.incarnation` on lease, and in the same tx marks older-incarnation running runs `interrupted`.
Owns: `managedworker/identity.go`(+test), `CP/incarnation.go`(+test).
Check: `go test -race ./internal/managedworker ./internal/controlplane -run 'TestInstanceIDPersists|TestIncarnationInterrupts'`. Deps: S0.

**S6 Config isolation + `machinist config validate` (D20).** Two-phase load: syntax errors keep the last good config; a bad entry becomes `InvalidCommand{Name, Reason "file:line:col: …"}` and shows in `/status invalid_commands[]`. CLI: `machinist config validate [--config PATH] [--json]`, exit 0/1.
Owns: `internal/config/validate.go`(+test), `internal/cli/config_cmd.go`(+test); 1 line in `root.go`.
Check: `go test -race ./internal/config ./internal/cli -run 'TestPerEntryValidation|TestConfigValidateCmd'` + `go run ./cmd/machinist config validate --config examples/config.toml`. Deps: S0.

**S7 Login hysteresis (D5).** `authFSM.Observe(result, now) → (state, recheckAt)`: first soft failure → `rechecking` with a fast recheck, second consecutive → `expired`, one success → `ok`. A past `expires_at` or a 401/logout expires at once. Appends an `auth_change` event.
Owns: `CP/auth_hysteresis.go`(+table test); edits `CP/auth_store.go` call site.
Check: `go test -race ./internal/controlplane -run TestAuthHysteresis` (synctest). Deps: S0.

**S8 Capped reader + shape-based usage (D1, D2).** `runner.LineReader(r, maxLine=1MiB)` → lines with a truncation flag. `usage.Detect(line) (Usage, bool)` picks the parser by JSON event shape (claude `result`/`message_delta`, codex `turn.completed`); argv is only a hint. Contract injection is unchanged. The CP writes `run_usage` on complete.
Owns: `runner/linereader.go`, `runner/usage_shape.go`, `runner/testdata/streams/*.jsonl` (golden, `-update`); edits `runner/codex_usage.go`; `CP/usage_store.go`.
Check: `go test -race ./internal/runner -run 'TestLineReader|TestUsageShape'` (includes an `agent-run --contract` wrapped fixture and a >64 KiB line) + `go test ./internal/controlplane -run TestRunUsageWritten`. Deps: S0.

**S9 Rate-limit classifier (D3).** `ratelimit.Classify(lines, exitCode, now, prevBackoff) → (Result{Limited, ResetAt, Source: structured|regex|estimate}, ok)`, ordered: structured → vendor regex table → capped backoff (60s ×2, cap 1h). Sets `Completion.FailureClass="rate_limited"`.
Owns: `runner/ratelimit.go`, `runner/ratelimit_table.go`, `runner/testdata/ratelimit/*`.
Check: `go test -race ./internal/runner -run TestClassifyRateLimit`. Deps: S8 (line reader).

**S10 Rate-limit store, lease skip, requeue (D4, D15).** In `complete` with `failure_class=rate_limited`, one `BEGIN IMMEDIATE` tx upserts `executor_state.unavailable_until`, requeues the job as an infra retry (it doesn't count against the attempt budget), and appends `rate_limit_stall_start`. The lease query skips executors with `unavailable_until > now`, and the first lease after expiry appends `rate_limit_stall_end`.
Owns: `CP/ratelimit_store.go`(+test).
Check: `go test -race ./internal/controlplane -run 'TestRateLimitRequeue|TestLeaseSkipsLimitedExecutor'`. Deps: S0, S9 (contract only; the test can use fake completions).

**S11 Executors in /status + operator clear + badge (D10, D26).** Fills `/status executors[]` exactly per the S2 v-schema block (`{id, worker, executor, state: ok|rate_limited, since, reason, message, rate_limited_until, reset_source}`), from `executor_state`. `POST api/executors/{worker}/{executor}/clear-rate-limit` (authorizeSubmission) → 200, logged as a `rate_limit_stall_end` event with cause `operator`. UI: an executor badge reading "rate limited until HH:MM (est.)" with a Clear button.
Owns: `CP/executors_http.go`(+test), `WEB/executor-badge.jsx`, `WEB/executor-badge.test.js`.
Check: `go test -race ./internal/controlplane -run TestExecutorClear` + `node --test src/executor-badge.test.js`. Deps: S2, S10, S3.

**S12 Labels + supersede (D12, D13).** Submit body `{labels:{ticket,round,branch,…}, supersedes_job_id}`.
*Contract for Scope B:* `machinist submit --label k=v` (repeatable; later duplicate key wins) and `--supersedes JOB_ID`. The dispatcher uses the label keys `ticket`, `round`, `branch`; these three names are fixed, other keys are free-form. In one `BEGIN IMMEDIATE` tx: insert job, insert labels, cancel the superseded job (409 if it already finished other than cancelled). Summaries carry `labels` and derived `superseded`. The CLI flags above map 1:1 onto the body.
Owns: `CP/labels.go`(+test); submit-flag lines in `root.go`.
Check: `go test -race ./internal/controlplane ./internal/cli -run 'TestSubmitLabels|TestSupersedeAtomic|TestSubmitLabelFlags'`. Deps: S2, S4 (cancel helper).

**S13 Ticket board UI.** Group-by toggle (none/ticket) with `sticky top-0` group headers. Superseded cancelled jobs are hidden behind a "show superseded" toggle. The Finished column is capped, and the mobile bottom nav uses safe-area padding and `min-h-dvh` (D19).
Owns: `WEB/ticket-board.js`(+test); edits `runs-board.js`.
Check: `node --test src/ticket-board.test.js src/runs-board.test.js`. Deps: S3, S12.

**S14 Needs-me (D10, D11).** Server: `attention_reason`, `waiting_since` on summaries; fills `/status gates_awaiting_approval[]`, `blocked_jobs[]`, `logins[]` with `Item {id, state, since, reason, message, html_url}` exactly per the S2 v-schema block (`connections[]` untouched). UI: an icon+text tone (not color alone), a "Needs me" filter, reason and relative age via `Intl.RelativeTimeFormat`, and a `document.title` of `(N) Machinist`.
Owns: `CP/attention.go`(+test), `WEB/needs-me.js`(+test).
Check: `go test -race ./internal/controlplane -run TestAttentionDerived` + `node --test src/needs-me.test.js`. Deps: S2, S3, S7 (logins state).

**S15 Gate review panel (D18, D19).** `/jobs/:id/gate` renders the gated artifact via `react-markdown` (no `rehype-raw`), with a sticky bottom action bar (Approve / Request changes, using the existing action route). `html_url` in S14 links here.
Owns: `WEB/gate-panel.jsx`, `WEB/gate-panel.test.js`; `package.json` + lockfile.
Check: `node --test src/gate-panel.test.js` (a `<script>` in the markdown renders as text) + `just frontend`. Deps: S3, S14.

**S16 Live log backend (D16).** Runner log ring → worker `POST api/runs/{id}/log` body `LogChunk` (≤16 KiB, excess dropped behind a truncation marker), separate from the heartbeat. The CP appends to `run_log_tail` (capped 256 KiB, ignores offsets it has already seen) and serves `GET api/runs/{id}/log?offset=N → {offset, next_offset, data, truncated, done}` behind authorizeArtifact.
Owns: `runner/logring.go`(+test), `managedworker/logship.go`(+test), `CP/runlog.go`(+test).
Check: `go test -race ./internal/runner ./internal/managedworker ./internal/controlplane -run 'TestLogRing|TestLogShip|TestRunLogOffsets'`. Deps: S0, S8, S2.

**S17 Run detail page + log pane.** `/runs/:id` shows result, events, rendered prompt and a log pane polling `?offset=` via `usePolling`, which stops when `done`.
Owns: `WEB/run-detail.jsx`, `WEB/run-log.js`(+test).
Check: `node --test src/run-log.test.js`. Deps: S3, S16.

**S18 Usage rollups (D14).** `GET api/usage?group_by=executor|model|command|ticket&since=` → `[{key, runs, input_tokens, output_tokens, cached_input_tokens, reasoning_tokens}]` (GROUP BY on read; `ticket` joins `job_labels`). UI `/usage` table.
Owns: `CP/usage_http.go`(+test), `WEB/usage.jsx`, `WEB/usage-state.js`(+test).
Check: `go test -race ./internal/controlplane -run TestUsageRollup` + `node --test src/usage-state.test.js`. Deps: S8, S12, S3.

**S19 Rewind / retry-to-step (D21, E8 part 1).** Migration 7: `workflow_attempts.attempt`, `previous_run_id`, `reason (rewind|loop|retry)`, `workflow_loop_counters(job_id, edge, count)`. `POST api/jobs/{id}/rewind {step, reason}` → 202; it appends rows and never mutates finished ones.
Owns: `CP/rewind.go`(+test), migration 7 entry in `store.go`.
Check: `go test -race ./internal/controlplane -run 'TestRewindAppends|TestMigration7'`. Deps: S12, S4.

**S20 `on_blocked = {goto, max}` (fork-only, E8 part 2).** A workflow TOML step field. When blocked, the loop counter is checked and incremented in the insert tx; at `max` the job stays blocked.
Owns: `internal/config/on_blocked.go`(+test), `CP/on_blocked.go`(+test).
Check: `go test -race ./internal/config ./internal/controlplane -run 'TestOnBlockedGoto|TestOnBlockedMax'`. Deps: S19.

## 3. Dependency graph and parallel lanes

```
S0 ─┬─ S1 ─ S2 ─┬─ S3 ─┬─ S13 (needs S12)      S17 (needs S16)
    │           │      ├─ S14 (needs S7) ─ S15
    │           │      └─ S11 (needs S10)
    │           ├─ S12 (needs S4) ─┬─ S18 (needs S8)
    │           │                  └─ S19 ─ S20
    │           └─ S16 (needs S8)
    ├─ S4   ├─ S5   ├─ S6   ├─ S7
    └─ S8 ─ S9 ─ S10
```
After S0, lane **ops** (S4, S5, S6, S7) and lane **runner** (S8 → S9 → S10, S16) run in parallel with lane
**read/UI** (S1 → S2 → S3). S19–S20 are strictly last. Merge order follows slice numbers where edges exist.
