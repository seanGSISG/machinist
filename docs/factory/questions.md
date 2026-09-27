# Research questions (Scope A: seanGSISG/machinist)

Each question states the decision it informs and the hard constraints from CONTEXT.md that bound the answer.

## Language / runtime

1. **Which standard-library and existing-module facilities in the repo's Go version are best suited to parsing and classifying streamed CLI output (token usage lines, usage-limit / HTTP 429 messages) from a child process that may be wrapped by another launcher?**
   - Decision: how usage parsing and `rate_limited` failure classification are implemented, and whether wrapper detection is by argv, env, or output shape.
   - Constraints: Go 1.26.6; wrapper detection is for usage parsing only and must not re-inject contract or revision text; `go test -race` must pass.

2. **What are the established patterns for a stable process/instance identity that survives restarts on the same host, and for detecting that a previous instance's in-flight work is orphaned?**
   - Decision: how a worker instance ID is derived and persisted, and how the control plane interrupts runs owned by a restarted worker.
   - Constraints: must be testable offline with no homelab access; no ticket touches Colo.

## Frameworks (backend HTTP + frontend UI)

3. **What request-handling and HTTP-semantics conventions keep read-only (GET) endpoints free of side effects while supporting list, detail and capped-history responses, and what status codes should idempotent cancel operations return for already-finished or unknown jobs?**
   - Decision: shape of the job list vs. job detail vs. run detail endpoints, cancel semantics, and client handling of 404/409 without exiting.
   - Constraints: existing Go control-plane API under `/api/v1`; generic fixes should stay small and upstreamable.

4. **What techniques does the existing frontend stack support for pausing periodic polling when a browser tab is hidden, rendering untrusted markdown safely, and building a sticky action bar and grouped board view that work on small mobile viewports?**
   - Decision: libraries/approach for markdown rendering, visibility-aware polling, and responsive layout of the inbox, gate panel and board.
   - Constraints: React/Vite in `internal/controlplane/web/src`; the built bundle is not committed — tickets change `web/src` and tests only; `npm test` + build are part of `just check`.

5. **Which approaches exist for delivering a live, incrementally growing log tail from a remote worker to the control plane and then to a browser (piggybacking on periodic heartbeats vs. separate upload vs. push channels), and what are their bounds on memory, ordering and truncation?**
   - Decision: transport and buffering for live run output and the live log pane.
   - Constraints: workers already heartbeat to the control plane; no inbound ports on the host; tests must fake all HTTP.

## Data storage

6. **What schema and transaction patterns in the control plane's current persistence layer support adding free-form job labels, a "supersedes" relation that atomically cancels the older job, per-executor "unavailable until" timestamps, and rollups of token usage by executor/model/command/label?**
   - Decision: table/column design, migrations, indexing, and whether aggregates are computed on read or maintained incrementally.
   - Constraints: must run under `go test -race` offline; finished-job history is capped; changes must not break upstream-compatible data where avoidable.

7. **How should a workflow engine represent "rewind to step N" and bounded loop-back (`goto` with a max count) without corrupting run history or artifact lineage?**
   - Decision: data model and state-machine changes for retry-to-step and `on_blocked` handling.
   - Constraints: control flow lives in code, not prompts; this is a fork-only feature and depends on job labels/supersedes existing first.

## Hosting / deployment

8. **How can configuration errors in one command or prompt definition be isolated at load time so a long-running service stays up, and how do comparable tools expose an offline "validate config" CLI subcommand with useful exit codes and messages?**
   - Decision: config loading error model (per-entry invalid state vs. fail-fast) and the `config validate` UX.
   - Constraints: must not crash-loop the control plane; deploys are human batches, so validation must be runnable before a restart; no ticket deploys anything.

## Auth

9. **What hysteresis/debounce strategies are used for health or credential-status checks to avoid flapping on transient failures, while still reacting immediately to a definitive signal such as a clearly past expiry time?**
   - Decision: the state machine for CLI login status (soft-failure counting, quick recheck interval, hard-expiry override).
   - Constraints: 2 consecutive soft failures with a quick recheck before marking expired; offline tests with fakes.

## Integrations

10. **What fields and semantics should a machine-readable status contract expose so an external consumer can detect gates awaiting approval, blocked jobs, expired logins and rate-limited executors, with stable identifiers and deep links?**
    - Decision: `/status` and job-detail payload shape consumed by external notifiers and a dispatcher.
    - Constraints: a separate repo codes against this contract with fakes; the status payload is being slimmed (no prompt/spec in the list); only this repo is ticketed here.

11. **How do agent CLIs commonly report usage-limit / rate-limit conditions and reset times (exit codes, stderr text, structured output), and how reliably can a reset time be extracted?**
    - Decision: the classifier rules and how `rate_limited_until` is computed when no explicit reset time is given.
    - Constraints: requeue as an infrastructure retry, not a ticket failure; tests use recorded fixtures, no real credentials.

## Testing

12. **What testing approaches fit a Go HTTP control plane plus a React UI when all external systems must be faked: HTTP handler tests, golden fixtures for CLI output, race-safe concurrency tests for leasing/cancel, and component tests for polling and visibility behaviour?**
    - Decision: test harness structure and fixture strategy per ticket.
    - Constraints: tests decide merges; gate is `just check` (frontend tests + build, gofmt, Python evals, vet, `go test -race`, build); no network; no edits to `.github/workflows/`.

13. **How can work be partitioned so many small tickets touching the same API types, UI state and shared docs can be developed in parallel with minimal merge conflicts?**
    - Decision: ticket boundaries, ordering (e.g. labels before rewind), and which shared files are declared as owned.
    - Constraints: target roughly 12–16 small/medium independent tickets; shared files like README.md/ARCHITECTURE.md/docs indexes go in `owns`; HANDOFF.md and STATUS.md are not edited.

## Observability

14. **What are good practices for surfacing "needs attention" state to an operator in a web dashboard (distinct visual tone, reason and age, counts in the document title, hiding superseded attempts) without adding server-side side effects?**
    - Decision: which derived fields the API provides vs. what the UI computes, and the inbox/filter design.
    - Constraints: GETs must be side-effect free; UI polling pauses when hidden; mobile layout must not clip.

15. **Which metrics and events should the engine record about executor rate-limit stalls, auth-status transitions, cancels and token usage so that per-ticket attended/unattended outcomes and stalls can be counted downstream?**
    - Decision: what the engine logs/exposes vs. what external metrics tooling derives.
    - Constraints: the ≥70% unattended-merge bar is measured externally; the engine must expose enough to count stalls and usage per `ticket` label.
