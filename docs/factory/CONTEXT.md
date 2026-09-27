**This plan run: Scope A, repo seanGSISG/machinist. Plan tickets ONLY for Scope A.** Scope B is planned separately in agent-factory; you may reference its contracts, never ticket them.

# CONTEXT: FAC-06, the factory improves itself

Grill session with Sean, 2026-09-27. Research: `docs/fac-06/research/` (remaining-issues.md = R-items,
machinist-audit.md = E-items, agent-deck.md). FAC-06's bar is **≥70% of tickets merged unattended**, on a
bigger and more parallel project than FAC-01. The project is the factory's own backlog, in two repos:

- **A: `seanGSISG/machinist`** (Sean's fork of owainlewis/machinist): engine + web UI. Go 1.26.6, React/Vite UI in
  `internal/controlplane/web/src`. Gate: `just check` (frontend `npm test` + build, gofmt, Python evals, vet,
  `go test -race`, build).
- **B: `seanGSISG/agent-factory`**: dispatcher, tickets, metrics (Go), worker step scripts (bash), prompts.
  Gate: `make check`.

Each repo gets its **own plan-project run**. The spec says which one; plan tickets only for that repo.

## Decisions
1. **Fork may diverge from upstream where it pays.** Generic fixes stay small and upstreamable. Fork-only
   features are fine.
2. **The web UI bundle is not committed** (machinist PR #6). UI tickets change `web/src` and tests only, never `web/dist`.
3. **Human-touch metric (lenient):** a ticket is *attended* if it got `needs-human` or its PR was merged by hand.
   `factory:retry` labels, issue edits and Machinist approvals are counted and printed, but don't mark a ticket attended.
4. **Alerts go to Telegram, with replies.** A dedicated factory bot (not the Hermes bots) long-polls from Colo
   (no inbound port), is allowlisted to Sean's Telegram user ID, and fails closed. It pushes batched events,
   each with a job link: gate waiting, needs-human, executor rate-limited, CLI login expired. Buttons:
   **Approve** (gate), **Request changes** (Sean's reply text becomes the feedback), **Retry ticket** (adds
   `factory:retry`), **Open**. Code decides every action. There is no LLM in this path. The web UI also gets an
   in-app "needs me" inbox.
5. **agent-deck: borrow features, don't integrate.** Rate-limit awareness, live run output, the attention
   inbox, keyboard/mobile polish.
6. **Deploys are human batches** between waves, with the dispatcher paused. No ticket deploys anything or
   touches Colo. Acceptance runs in the ticket's worktree.

## Scope A: machinist (engine + UI)
- **E1/R1b** Token usage for wrapped claude: detect claude behind `agent-run --contract` **for usage parsing only**.
  It must not re-inject the contract or revision text.
- **E2/R3** Auth status hysteresis: expire after 2 consecutive soft failures with a quick recheck. A clearly
  past expiry still expires at once.
- **E3/R9** Cancel direct `machinist run` jobs (404 today). A worker must not exit on a cancel 404/409.
- **E11/R10** One bad prompt/command config marks that command invalid instead of crash-looping the control
  plane. Add `machinist config validate`.
- **E4** Status diet: drop prompt/spec from the `/status` job list, add `GET /api/v1/jobs/{id}`, cap finished
  jobs, make GETs side-effect free, and pause UI polling in a hidden tab.
- **E5** "Needs me" inbox: a distinct tone for blocked/awaiting-approval jobs, a filter, reason and age on
  cards, a count in the tab title, and superseded cancelled attempts hidden.
- **E6** Gate review panel: rendered markdown artifacts, the gated artifact shown next to Approve/Request
  changes, and a sticky action bar. A revision diff is optional/later.
- **E7** Job labels (`ticket`, `round`, `branch`) + a `supersedes` link (cancel the old job in the same
  transaction) + a ticket-grouped board view.
- **E9** Run detail endpoint (result, events, rendered prompt) + a live log tail sent with heartbeats + a live log pane.
- **E10** Rate-limit awareness: classify 429/usage-limit output as `failure_class=rate_limited`, mark the executor
  `rate_limited_until`, stop leasing to it until then, requeue as an infrastructure retry, and show it in `/status` and the UI.
- **E12** Token rollups per executor/model/command and per `ticket` label.
- **E13** Mobile: the bottom nav clips; cap the Finished column.
- **E14** Stable worker instance ID; interrupt a restarted worker's runs at once.
- **E8 (last)** `rewind`/retry-to-step API, then a fork-only workflow `on_blocked = {goto, max}`. Only after E7.

## Scope B: agent-factory
- **R2** factory-metrics: a GitHub-side per-ticket ledger (timeline labels, issue edits, merge mode) with the
  lenient rule above. Flags: `-github-repo`, `-github-fixture` (recorded JSON for tests), `-status-file`,
  `-manual-merges-before`. Print every component and the rule applied.
- **R4** Health probe in the repo: all 4 workers, dispatchers and CLI logins → Kuma push. It replaces the
  unversioned script on Colo.
- **R7** Union files per target repo (dispatch env/flag) instead of one global list.
- **R8** Deploy script (backup, install, validate, restart, rollback) + a version stamp (`-ldflags`) in all
  factory binaries. The script is written and tested here; a human runs it.
- **R11** factory-verify splits `cmd -> expect` at the **last** ` -> ` (or a stricter rule), with tests.
- **Telegram bot** (decision 4): `cmd/factory-bot`. Telegram client behind an interface with a fake in tests.
  Event sources are the Machinist `/status` (gates, logins, rate limits) and dispatcher state (needs-human).
  Batching, dedupe, allowlist, fail-closed. Actions call Machinist approve/request-changes and add GitHub labels.
- **Dispatcher, rate limits:** a run with `failure_class=rate_limited` parks the ticket until
  `rate_limited_until`, without spending a fix round or escalating. Metrics count the stalls. Code this
  against the contract in Scope A (E10) with fakes.

## Constraints
- **No ticket edits `.github/workflows/`.** The GitHub token lacks the `workflow` scope; CI changes go to Sean.
- Tests are offline: fake GitHub, Telegram and Machinist HTTP. No real credentials, no network to the homelab.
- Hard rules stand: control flow in code, stage prompts ≤2 KB/40 lines, tests decide merges.
- Shared files that several tickets append to (README.md, ARCHITECTURE.md, docs indexes) go in `owns`.
  HANDOFF.md and STATUS.md are Sean's/the operator's; tickets don't edit them.
- Prefer many small, independent tickets (S/M). FAC-06 measures parallel fan-out. Target is about 12–16 tickets for A and 8–10 for B.

## Out of the factory (the operator does these)
Upstream PRs to owainlewis (R5), UniFi key rotation (R6), Colo housekeeping (R12), creating the Telegram bot
in BotFather, CI changes, and all Colo deploys.
