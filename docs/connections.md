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
| `path` | worker's `PATH` | `PATH` for the login and status commands. |
| `timeout` | `10m` | Login time limit (30s to 1h). The terminal is killed when it expires. |
| `status_interval` | `5m` | How often the status command runs (at least 30s). |

A recipe may define only `status` (health checks, no Connect button) or only
`login` (always *unknown*, never gated).

## States and gating

The worker runs `status` at start-up, every `status_interval`, and right after
each login. It reports one state per executor:

| State | Meaning |
|---|---|
| connected | Status exited 0 (and matched `connected_pattern`, if set). |
| expiring | Connected, but `expires_pattern` found an expiry inside `expiring_within`. |
| expired | Status exited non-zero, did not match `connected_pattern`, or the expiry has passed. |
| unknown | No status command, it could not start, or it timed out (30s). |

An **expired** executor appears in the *Needs attention* column of the Tasks
board, and the control plane does not lease new runs for it to that worker.
Queued runs wait until someone reconnects. Running runs are not interrupted.
*Unknown* and *expiring* executors are still leased.

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
- Terminal output stays on the worker. What it reports is redacted: links,
  anything typed into the terminal, and token-like strings (32+ characters)
  are replaced, and only the last 4 KiB is kept. Status output is parsed and
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
| kimi | `kimi login --region global` | device code | none yet |
| omp | `omp login anthropic` | provider OAuth, link + paste | none yet |
| opencode | `opencode auth login` | provider picker, then OAuth | `opencode auth list` |
| pi | TUI `/login` via `start_input` | provider picker, then OAuth | `pi auth check --provider anthropic --json --no-refresh` |

For pickers (opencode, pi), type the provider name in the box and send it, or
use the arrow and Enter buttons; the redacted terminal output shows the menu.
