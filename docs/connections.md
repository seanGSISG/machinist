# Connections: log in to agent CLIs from the web UI

The **Connections** page logs each worker's subscription CLIs (Claude Code,
Codex, Kimi, omp, opencode, pi) in without SSH. The login runs on the worker,
as the worker user, and the CLI stores its own credentials there. Nothing that
could authenticate as you leaves the worker: each host keeps its own refresh
token, so hosts never log each other out.

Authority stays local, as with executors. `worker.toml` declares the login and
status commands. The control plane only names an executor; it can never
supply or change a command.

## Configure a recipe

Add an optional `auth` table to an executor in `worker.toml`:

```toml
[executors.claude]
command = ["claude", "--print", "--verbose", "--output-format", "stream-json", "--model={{machinist.model}}", "--dangerously-skip-permissions"]

[executors.claude.auth]
login = ["claude", "auth", "login", "--claudeai"]
status = ["claude", "auth", "status"]
connected_pattern = '"loggedIn":\s*true'
prompt_pattern = '(?i)paste\s+(the\s+)?code'
```

| Field | Default | Meaning |
|---|---|---|
| `login` | none | Argument array that starts an interactive login. Runs in a pseudo-terminal. |
| `status` | none | Argument array that exits 0 while the CLI is logged in. Runs without a terminal. |
| `connected_pattern` | none | Regex that must also match the status output (for commands that exit 0 when logged out). |
| `expires_pattern` | none | Regex whose first group captures the expiry as RFC 3339 or Unix seconds/milliseconds. |
| `expiring_within` | `72h` | With `expires_pattern`: report *expiring* when the login ends within this window. |
| `url_pattern` | `https://[^\s"'<>]+` | Regex for the login link in the output. Only `https` links are shown. |
| `code_pattern` | none | Regex for a device code (first group, or the whole match). |
| `prompt_pattern` | none | Regex that means the CLI is waiting for a pasted code. |
| `start_input` | none | Strings typed after start, for TUIs whose login is a slash command (`["/login\r"]`). |
| `path` | worker's `PATH` | `PATH` for the login and status commands. Must include the runtime of script CLIs (see below). |
| `timeout` | `10m` | Login time limit (30s to 1h). The terminal is killed when it expires. |
| `status_interval` | `5m` | How often the status command runs (at least 30s). |

A recipe may define only `status` (health checks, no Connect button) or only
`login` (always *unknown*, never gated).

### `path` must cover the CLI's runtime

Recipes run with a minimal environment (see [The login flow](#the-login-flow)),
so `PATH` is exactly `path`, or the worker's own `PATH` when `path` is unset.
CLIs installed with npm, such as `codex` and `pi`, are `#!/usr/bin/env node`
scripts. If the directory holding `node` is not on that `PATH`, the status
check fails with `/usr/bin/env: 'node': No such file or directory` (exit code
127) even though the CLI is logged in. Put both directories in `path`:

```toml
path = "/opt/machinist/bin:/opt/machinist/toolchains/node-v22.22.2-linux-x64/bin:/usr/local/bin:/usr/bin:/bin"
```

A status command that exits 126 or 127 (command or runtime not found) is
reported as *unknown* with a hint to check `path`, not as *expired*, so a
recipe mistake never holds runs.

### Executors that share one login

Several executors often wrap the same CLI with different models (`claude` and
`claude-opus`; `codex`, `codex-sol` and `codex-pool`). They share that CLI's
credentials, so:

- Put `login` on **one** executor per CLI (the plain `claude`, `codex`). That
  gives one Connect button per login and no two logins racing to write the
  same credential file.
- Put the same `status` (and `path`, `connected_pattern`) on **every**
  executor that shares the login, so each one is gated when the login
  expires. The status commands only read local state, so the extra checks
  are cheap.
- When any login ends, the worker re-checks every executor at once, so the
  executors sharing that login turn *connected* together.

## States and gating

The worker runs `status` at start-up, every `status_interval`, and right after
each login. It reports one state per executor:

| State | Meaning |
|---|---|
| connected | Status exited 0 (and matched `connected_pattern`, if set). |
| expiring | Connected, but `expires_pattern` found an expiry inside `expiring_within`. |
| expired | Status exited non-zero, did not match `connected_pattern`, or the expiry has passed. |
| unknown | No status command yet (including before the first check), it could not start, exited 126/127, or timed out (30s). |

An **expired** executor appears in the *Needs attention* column of the Tasks
board, and the control plane does not lease new runs for it to that worker.
Queued runs wait until someone reconnects. Running runs are not interrupted.
*Unknown* and *expiring* executors are still leased.

Every executor starts as *unknown* until its first status check finishes
(normally within seconds of worker start). A run leased in that window can go
to an executor whose login has in fact expired; it fails like any run with a
logged-out CLI. This window is accepted rather than blocking every run until
the first check.

CLIs without a status command (kimi and omp today) stay *unknown*: they are
never blocked, and a logged-out CLI shows up only as failed runs. Connect
still works for them.

## The login flow

1. **Connect** sends `POST /api/v1/connections/{worker}/{executor}/login`.
   The control plane returns a session and a single-use token.
2. On its next sync (at most 2 seconds), the worker starts `login` in a
   pseudo-terminal: working directory = the worker user's home directory,
   environment = only `HOME`, `USER`, `LOGNAME`, `PATH`, `TERM` and `LANG`
   (like `env -i`). This stops a CLI from reading another user's project
   settings or inheriting the worker's secrets.
3. The worker reads the output, extracts the login link, the device code and
   whether the CLI waits for a pasted code, and reports them.
4. The page shows **Open login page**, the code, and a paste box. Text sent
   from the box is typed into the terminal followed by Enter. Enter, arrow,
   Tab and Esc buttons drive provider pickers.
5. The session ends when the CLI exits (0 = succeeded), on Cancel, or on
   timeout; the worker kills the process group in the last two cases, then
   re-runs `status`.

## Security

- Login mutations (`start`, `input`, `cancel`) use the same checks as the
  settings API: the browser needs the CSRF token and a loopback `Origin`, or
  the client sends the worker bearer token.
- Each session is single-use and bound to the tab that started it. Start
  returns a random token (only its SHA-256 is kept); reading the session,
  sending input and cancelling require it in `X-Machinist-Login-Token`. The
  page keeps it in memory only. Another tab can only *Start over*, which
  cancels the old session.
- Login sessions live only in control plane memory. Links, device codes and
  pasted input are never written to the database or logs. Pasted input is
  held until the worker's next sync, then dropped. Finished sessions are
  forgotten after 10 minutes, and a restart ends them all.
- The worker never runs an argument from the control plane. A start for an
  executor without a `login` recipe fails on the worker.
- A session is bound to one worker instance, the one that reported last for
  that worker name. During a restart, when an old and a new instance share a
  name, only that instance receives the session's actions or can report on it.
- Terminal output stays on the worker. What it reports is redacted: links,
  text pasted from the box (3 characters or more), and token-like strings
  (32+ characters) are replaced, and only the last 4 KiB is kept. Shorter
  input, such as a picker choice like `1` or `y`, stays visible: redacting it
  would blank every matching character in the transcript, and no code or
  password is that short. Key buttons and `start_input` are not redacted. Status output is parsed and
  discarded; only the state and a short detail are reported.
- Worker logs record only `login <id> for <executor>: started|succeeded|...`.

## API

| Method and path | Auth | Purpose |
|---|---|---|
| `GET /api/v1/connections` | none (loopback, like `GET /api/v1/settings`) | Auth state per worker and executor, plus any active session id. |
| `POST /api/v1/connections/{worker}/{executor}/login` | CSRF or bearer | Start a login. Body `{"replace": false}`. Returns `{session, token}`. |
| `GET /api/v1/connections/sessions/{id}` | login token | Link, code, prompt state, redacted transcript. |
| `POST /api/v1/connections/sessions/{id}/input` | CSRF or bearer + login token | `{"text": "..."}` or `{"key": "enter"}`. |
| `POST /api/v1/connections/sessions/{id}/cancel` | CSRF or bearer + login token | Cancel the login. |
| `POST /api/v1/workers/auth` | worker token | Worker sync: states and session reports in, queued actions out. |

## Example recipes

From a spike on 2026-09-27 (claude 2.1.283, codex 0.153.3, kimi 2.1.1,
omp 18.3.3, opencode 1.18.32, pi 0.87.1). The status commands were checked;
the login flows were not started, so confirm each pattern with a first
Connect and adjust it. The full set is in
[`examples/auth-recipes.toml`](../examples/auth-recipes.toml).

| CLI | Login | Flow | Status |
|---|---|---|---|
| claude | `claude auth login --claudeai` | link + paste-back code | `claude auth status` (JSON `loggedIn`) |
| codex | `codex login --device-auth` | device code | `codex login status` ("Logged in using ...") |
| kimi | `kimi login --region global` | device code | none (stays *unknown*) |
| omp | `omp login anthropic` | provider OAuth, link + paste | none (stays *unknown*) |
| opencode | `opencode auth login` | provider picker, then OAuth | `opencode auth list` (at least one `oauth`/`api` line) |
| pi | TUI `/login` via `start_input` | provider picker, then OAuth | `pi auth check --provider PROVIDER --json --no-refresh` |

Notes on the status commands:

- `codex login status` prints `Logged in using ChatGPT` and exits 0. It needs
  `node` on `path`, as does pi.
- `opencode auth list` exits 0 even with no credentials, so the recipe's
  `connected_pattern` requires at least one credential line (for example
  `OpenAI oauth`).
- `pi auth check` checks **one provider**. It prints `{"status":"ready",...}`
  and exits 0 when that provider is logged in, and
  `{"status":"not_ready","reason":"credentials_not_configured"}` with exit 1
  when it is not. Set `--provider` to the provider the executor actually uses
  (`pi`'s `defaultProvider` is not necessarily the logged-in one). A provider
  that was never configured marks pi *expired* and holds its runs, so the
  example leaves pi's `status` commented out until you replace `PROVIDER`.


For pickers (opencode, pi), type the provider name in the box and send it, or
use the arrow and Enter buttons; the redacted terminal output shows the menu.

## Example: a worker with shared logins

A worker whose executors are all wrapped as `/opt/machinist/bin/agent-run --
<cli> ...`, with the CLIs in `/opt/machinist/bin` and node in a private
toolchain directory. The recipes call the CLIs directly; `agent-run` only
wraps agent runs. Executor `command` lines are omitted.

```toml
[executors.claude.auth]
login = ["claude", "auth", "login", "--claudeai"]
status = ["claude", "auth", "status"]
connected_pattern = '"loggedIn":\s*true'
prompt_pattern = '(?i)paste\s+(the\s+)?code'
path = "/opt/machinist/bin:/usr/local/bin:/usr/bin:/bin"

[executors.claude-opus.auth]  # shares claude's login
status = ["claude", "auth", "status"]
connected_pattern = '"loggedIn":\s*true'
path = "/opt/machinist/bin:/usr/local/bin:/usr/bin:/bin"

[executors.codex.auth]
login = ["codex", "login", "--device-auth"]
status = ["codex", "login", "status"]
connected_pattern = 'Logged in using'
code_pattern = '\b([A-Z0-9]{4,}-[A-Z0-9]{4,})\b'
path = "/opt/machinist/bin:/opt/machinist/toolchains/node-v22.22.2-linux-x64/bin:/usr/local/bin:/usr/bin:/bin"

[executors.codex-sol.auth]  # shares codex's login
status = ["codex", "login", "status"]
connected_pattern = 'Logged in using'
path = "/opt/machinist/bin:/opt/machinist/toolchains/node-v22.22.2-linux-x64/bin:/usr/local/bin:/usr/bin:/bin"

[executors.codex-pool.auth]  # shares codex's login
status = ["codex", "login", "status"]
connected_pattern = 'Logged in using'
path = "/opt/machinist/bin:/opt/machinist/toolchains/node-v22.22.2-linux-x64/bin:/usr/local/bin:/usr/bin:/bin"

[executors.pi.auth]
login = ["pi"]
start_input = ["/login\r"]
status = ["pi", "auth", "check", "--provider", "openai-codex", "--json", "--no-refresh"]
connected_pattern = '"status":\s*"ready"'
prompt_pattern = '(?i)(paste|enter).{0,40}(code|url)'
path = "/opt/machinist/bin:/opt/machinist/toolchains/node-v22.22.2-linux-x64/bin:/usr/local/bin:/usr/bin:/bin"

[executors.omp.auth]  # no status command: stays unknown, never blocked
login = ["omp", "login", "anthropic"]
prompt_pattern = '(?i)(paste|enter).{0,40}(code|url)'
path = "/opt/machinist/bin:/opt/machinist/toolchains/node-v22.22.2-linux-x64/bin:/usr/local/bin:/usr/bin:/bin"

[executors.opencode.auth]
login = ["opencode", "auth", "login"]
status = ["opencode", "auth", "list"]
connected_pattern = '(?m)\b(oauth|api)\s*$'
path = "/opt/machinist/bin:/opt/machinist/toolchains/node-v22.22.2-linux-x64/bin:/usr/local/bin:/usr/bin:/bin"

[executors.kimi.auth]  # no status command: stays unknown, never blocked
login = ["kimi", "login", "--region", "global"]
code_pattern = '\b([A-Z0-9]{4,}-[A-Z0-9]{4,})\b'
path = "/opt/machinist/bin:/usr/local/bin:/usr/bin:/bin"
```

Set pi's `--provider` to the provider its executor runs on; `openai-codex` is
an example of a provider that is logged in.
