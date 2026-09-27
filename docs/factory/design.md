# Design: FAC-06 Scope A (seanGSISG/machinist)

Inputs: CONTEXT.md (Sean's decisions, grill session 2026-09-27) and research.md (R§n = research section n).
CONTEXT wins on any conflict, except where Sean's answers at gate 1 (2026-09-27, marked "Sean") override it.
Scope B (agent-factory) appears here only as a consumer of contracts.

## 1. Current state

- **Control plane** (`internal/controlplane`): stdlib `ServeMux` with method patterns. SQLite (modernc) in WAL mode.
  Hand-rolled migrations tracked by `PRAGMA user_version`, now at **5**.
- **Jobs and runs:** `jobs` holds `prompt` and trigger metadata. `runs` holds `rendered_prompt`, `result`,
  `events` and a single scalar `token_usage`. Workflows add `workflow_jobs` (plan, `current_step`) and
  `workflow_attempts(run_id, step, outcome, summary)`. That is already one run row per step attempt.
- **`GET /api/v1/status` has side effects.** It calls `maintainState` (reclaim expired leases, prune superseded
  workers) and then returns a full snapshot, including prompt and spec text for every job with no cap.
- **Actions:** the only action route is `POST /jobs/{id}/{action}`, which goes through `WorkflowAction`.
  Direct (`machinist run`) jobs aren't workflows, so cancelling one returns 404. A worker that gets a 404/409
  back on cancel or heartbeat can exit (E3/R9).
- **Usage:** the `runner/codex_usage.go` collector is chosen by argv: `codex exec --json`, or a claude command
  with json/stream-json output. Claude wrapped by `agent-run --contract` is not recognised, so no tokens are
  recorded (E1/R1b). The runner appends the workflow-contract text to the prompt itself.
- **Failures:** there is no `failure_class` and no rate-limit concept. A usage-limit run just fails.
- **Workers:** the `workers(instance_id, name, last_seen_at)` table exists. The instance ID isn't stable
  across restarts, so a restarted worker's old runs wait for their leases to expire (E14).
- **Logins:** `worker_executor_auth` plus the Connections UI (MCH-05). One soft failure flips a login to expired (E2/R3).
- **Config:** loading a bad command or prompt entry fails the whole load, so the control plane crash-loops (E11/R10).
- **UI:** React 19 / Vite 8 / Tailwind 4 in `web/src`, tested with `node --test` + jsdom. The polling in
  `status-loader.js` and `connections.jsx` uses `setInterval` and doesn't pause when the tab is hidden. There's
  no markdown renderer. On mobile the bottom nav clips and the Finished column is unbounded. `web/dist` isn't committed.

## 2. Desired end state (user-visible)

1. **"Needs me".** A distinct tone with an icon and text for blocked and awaiting-approval jobs, a filter, and
   a reason and age on each card. The tab title shows the count, e.g. "(3) Machinist". Superseded cancelled
   attempts are hidden by default.
2. **Gate review panel.** The gated artifact is rendered as markdown next to Approve / Request changes, in a
   sticky action bar that works on a phone.
3. **Ticket board.** Jobs carry `ticket`, `round` and `branch` labels. The board can group by ticket, and a new
   round supersedes the old job (the old one is cancelled atomically).
4. **Run detail and live log.** A run page shows the result, events and rendered prompt, plus a log pane that
   tails the output while the run is live.
5. **Rate limits are visible and respected.** An executor that hits a usage limit shows "rate limited until
   HH:MM" (an estimate when the CLI gave no reset time). No work is leased to it until then, and an operator
   can lift it early with a "Clear" button. The run is requeued as an
   infrastructure retry, not counted as a ticket failure.
6. **Token accounting.** Wrapped claude reports tokens. Rollups by executor, model, command and `ticket` label.
7. **Stable operations:**
   - login status no longer flaps;
   - cancelling a direct run works;
   - a worker survives cancel 404/409;
   - a restarted worker's orphaned runs are interrupted at once;
   - one bad config entry disables only that command, and `machinist config validate` reports it before a deploy.
8. **A cheaper `/status`.** Slim, capped and side-effect free, with `GET /jobs/{id}` for detail. A hidden tab stops polling.
9. **Last:** a `rewind` / retry-to-step API, then a fork-only workflow `on_blocked = {goto, max}`.

## 3. Resolved decisions

| # | Decision | Reason |
|---|---|---|
| D1 | Read stream output with `bufio.Reader`, capping and truncating each line (not `bufio.Scanner`). | Scanner stops for good on a line over 64 KiB (R§1). |
| D2 | Choose usage parsing by **output shape**: JSON lines of a known event type. Argv (`agent-run --contract`) is a hint only. Contract and revision injection stay exactly as they are. | Wrapper detection is most robust by output shape (R§1). CONTEXT E1 says "usage parsing only". |
| D3 | An ordered rate-limit classifier: structured reset (`resetsAt`, `retry-after`, `retryDelay`), then the vendor regex table, then (spend cap or no reset time found) a capped-backoff probe estimate. Each result records `reset_source`. | Reset times are only reliable when structured, and text varies by version (R§11). |
| D4 | `failure_class=rate_limited` sets `unavailable_until` on an executor-state row. The lease query skips that executor, and the run is requeued as an infrastructure retry. | Matches E10; nullable additive column (R§6). |
| D5 | Login hysteresis is a hand-rolled state machine: the first soft failure schedules a quick recheck, the second consecutive one expires, one success resets. A past `expires_at` or an explicit logout/401 expires at once. Clock and checker are injected. | This is the exact 2-failure rule, with HAProxy `fastinter` as the model (R§9). |
| D6 | Stable worker ID is a UUID persisted in the worker state dir. The control plane bumps an **incarnation** counter on registration, runs record their incarnation, and a new incarnation interrupts older in-flight runs. | Portable, testable offline, and never exposes machine-id (R§2). |
| D7 | GETs only read. Lease reclaim and worker pruning move to the existing background scheduler loop. | RFC 9110 safe methods (R§3). |
| D8 | `GET /jobs` (and the `/status` job list) returns slim summaries with no prompt or spec, finished jobs capped at 50 by default (configurable). `GET /jobs/{id}` returns detail with capped run history, and `GET /runs/{id}` returns run detail. No compat field for the removed text. | R§3 list/detail split; Sean: nothing in agent-factory or on Colo reads prompt/spec from `/status` (the dispatcher only sends a spec on submit). |
| D9 | Cancel codes: 202/200 while active, 200 if already cancelled (idempotent), 409 if finished another way, 404 if unknown. Direct jobs get their own cancel path. Workers treat 404 and 409 as terminal information. | GitHub/Stripe precedent, safe retries (R§3). |
| D10 | `/status` gains `schema_version` and `generated_at`, plus `gates_awaiting_approval[]`, `blocked_jobs[]`, `logins[]` and `executors[]` (with `rate_limited_until`). Items have an `id`, closed-enum `state`, `since`, CamelCase `reason` + `message`, and `html_url`. Changes are additive only. | This is the contract the Scope B bot and dispatcher code against with fakes (R§10). |
| D11 | The server derives `attention_reason`, `waiting_since` and `superseded` on read. The UI derives counts, title, ages (`Intl.RelativeTimeFormat`) and filtering. | One source of truth for the UI and the bot (R§14). |
| D12 | Labels go in a `job_labels(job_id,label_key,value)` join table with an index, written in the job's insert transaction. | Indexed filters and GROUP BY rollups (R§6). |
| D13 | `jobs.supersedes_job_id`. The new job is inserted and the old one cancelled in one `BEGIN IMMEDIATE` transaction. | Avoids SQLITE_BUSY on the read→write upgrade (R§6). |
| D14 | Usage rows per run (input, output, cached, reasoning), rolled up on read with GROUP BY. Column names map to `gen_ai.usage.*`. There are no aggregate tables. | Aggregates drift when dimensions are added (R§6, R§15). |
| D15 | An append-only `events` table (rate_limit_stall start/end, auth_change, cancel, usage) with a read-only API, mirrored to slog. No Prometheus or OTel. | Per-ticket cardinality belongs in a store, not metric labels (R§15). |
| D16 | The live log goes over a **dedicated worker endpoint** for log chunks, not the heartbeat. Chunks are offset-sequenced and capped (e.g. 16 KiB per request, excess dropped behind a truncation marker); the control plane keeps a capped tail per run and ignores already-seen offsets. The browser polls `GET /runs/{id}/log?offset=N`. | Separate chunk POSTs keep a log burst from delaying liveness and offsets give idempotent ordered delivery (R§5); Sean chose this over CONTEXT E9's "with heartbeats". SSE is deferred. |
| D17 | The UI polls through a custom visibility-aware `usePolling` hook that refetches when the tab becomes visible again. No TanStack Query or SWR. | No new dependency; matches the existing code (R§4a). |
| D18 | Markdown is rendered with react-markdown, **never** `rehype-raw`. | The only option that never touches innerHTML (R§4b). |
| D19 | Mobile layout uses plain Tailwind: a `sticky bottom-0` action bar with `env(safe-area-inset-bottom)` padding, `sticky top-0` group headers and `min-h-dvh`. | No dependency (R§4c). |
| D20 | Config loads in two phases: parse the file, then decode and validate each entry separately. A bad entry becomes an invalid command with a file:line:col reason. A file syntax error keeps the last good config. `machinist config validate [--json]` exits 0 if valid, 1 on errors. | Prometheus/nginx behaviour; go-toml gives positions (R§8). |
| D21 | Rewind is append-only. It inserts new run and attempt rows with `attempt`, `previous_run_id` and `reason` (rewind / loop / retry). A persisted per-(job, edge) loop counter is checked against `max` inside the insert transaction. `on_blocked goto` is fork-only and ships after labels and supersedes. | Preserves history, and control flow stays in code (R§7). CONTEXT orders E8 last. |
| D22 | Migrations continue the hand-rolled `user_version` sequence (6, 7, …). No goose or migrate. | No second version source (R§6). |
| D23 | Tests: `httptest` for handlers, `synctest` with in-memory fakes for timing, and golden CLI-stream fixtures with a hand-rolled `-update` flag. UI tests stay on `node --test`, jsdom and `mock.timers`. | Fits the `just check` gate, offline (R§12). |
| D24 | New Go files per feature, and a UI module per view. Shared structs gain only optional `omitempty` fields. The schema foundation lands before its dependants. | Minimizes merge conflicts under parallel fan-out (R§13). |
| D25 | Detail GETs that expose prompt or result text (`/jobs/{id}`, `/runs/{id}`, the log) use the same authorization wrapper as the artifact routes: worker token, or same-origin browser with CSRF. | Slimming `/status` must not leak the text through a new open path; Sean: the UI already satisfies this and the bot uses a token. |
| D26 | An operator "clear" action (API route plus a UI button on the executor badge) sets `unavailable_until` to null early and appends a `rate_limit_stall_end` event with the operator as cause. | Sean: no-reset limits fall back to the automatic backoff probe (D3), with a manual override. |
| D27 | `events`, `run_usage` and run log tails are kept 90 days by default (configurable). Pruning runs in the scheduler loop, never in a GET. | Sean; consistent with D7 (R§3 safe methods). |

## 4. Architecture sketch

```
 agent CLI stdout ──► runner: capped line reader (D1)
                        ├─ shape-based usage parser (D2) ──► usage fields in completion
                        ├─ rate-limit classifier (D3) ─────► failure_class, reset_at, reset_source
                        └─ log ring (offset) ──────────────► POST log chunk {offset, data} (D16)
 managedworker: persisted instance UUID (D6); treats cancel/heartbeat 404/409 as terminal (D9)
        │  poll / heartbeat / log chunk / complete   (outbound only)
        ▼
 control plane
   store (SQLite, migrations 6+):
     job_labels · jobs.supersedes_job_id · executor_state.unavailable_until
     workers.incarnation · runs.incarnation · run_usage · events · run_log_tail
     attempts: attempt / previous_run_id / reason / loop counters (E8)
   config loader: per-entry validation (D20) ──► invalid commands listed in /status
   scheduler loop: lease reclaim, worker prune, auth rechecks, 90-day retention prune (D5, D7, D27)
   lease query: skips rate-limited executors, stale incarnations
   read API (side-effect free): /status v-schema (D10) · /jobs · /jobs/{id} · /runs/{id}
                                /runs/{id}/log?offset · /usage rollups · /events
   write API: submit {labels, supersedes} · cancel (D9) · approve/request-changes · rewind (E8)
              · executor rate-limit clear (D26)   [detail + log GETs behind artifact auth (D25)]
        │
        ├──► web UI: usePolling (D17) → board (ticket groups) · needs-me inbox · gate panel
        │            (react-markdown) · run detail + log pane · usage rollups · executor badges
        └──► Scope B consumers (not built here): factory-bot reads /status and calls actions;
             the dispatcher reads failure_class / rate_limited_until and submits with labels + supersedes
```

Flow for a rate limit:
1. The run's output matches the classifier.
2. `complete` carries `failure_class=rate_limited` with its `reset_at` and `reset_source`.
3. In one transaction the store sets `executor_state.unavailable_until`, requeues the job as an infrastructure
   retry, and appends a `rate_limit_stall_start` event.
4. The lease query skips that executor until the time passes (or an operator clears it, D26). With no reset time,
   the backoff estimate expires and the next lease acts as the probe; another limit doubles the backoff up to the cap.
5. `/status` `executors[]` and the UI show it. The Scope B dispatcher parks the ticket.

Flow for a gate: a workflow step reaches `awaiting_approval`, and `/status` lists it in `gates_awaiting_approval[]`
with an `html_url` that deep-links to the gate panel. Approve or request-changes goes through the existing action route.

## 5. Patterns

**Follow:**
- Control flow in code; classifiers and state machines are tables with fixture tests.
- Additive API and schema only: optional `omitempty` fields, nullable columns, new tables. Consumers treat unknown enum values as unknown.
- Every multi-row state change is one transaction; supersede and rate-limit requeue use `BEGIN IMMEDIATE`.
- Derive on read (attention, superseded, rollups) rather than storing derived state.
- Inject clocks, checkers and HTTP; everything offline under `-race`.
- Cap everything that grows: line length, log tail, finished list, run history, per-request log chunk.
- Status is never conveyed by color alone (WCAG 1.4.1 / 1.4.11, R§14).
- Keep generic fixes (cancel codes, side-effect-free GET, config isolation, hysteresis, wrapped usage) small and
  upstreamable. Fork-only features (labels UI, `on_blocked goto`) live in their own files.

**Avoid:**
- Mutating state (including retention pruning) in any GET handler.
- Carrying log data on heartbeats; liveness traffic stays small.
- Prompt or spec text in list payloads.
- `rehype-raw`, `dangerouslySetInnerHTML`, marked without a sanitizer.
- Re-injecting contract or revision text when wrapped claude is detected.
- Treating a regex-derived reset time as authoritative when structured data exists.
- Aggregate or trigger-maintained rollup tables, Prometheus labels per ticket, OTel GenAI (still Development status).
- New dependencies beyond react-markdown: no TanStack Query, SWR, Vitest, goose or gobreaker.
- json/v2-only APIs (the repo stays on Go 1.26.6).
- Committing `web/dist`, editing `.github/workflows/`, HANDOFF.md or STATUS.md, or touching Colo.
- Mutating finished run rows (rewind appends).
