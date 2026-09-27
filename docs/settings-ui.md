# Settings in the web UI

The **Settings** page lets an operator change command defaults, workflows, and
per-executor default models from the browser. Changes are stored in the control
plane database with a versioned history and apply to new tasks only.

Authority stays local. `worker.toml` still declares which executables exist
(the argument allowlist), their model aliases, repositories, and credentials. The
UI chooses among what workers advertise; it can never create or change an
executor command.

## What can be managed

| Kind | Key | Fields (empty = inherit from `config.toml`) |
|---|---|---|
| Command | command name | `executor`, `model`, `timeout`, `prompt` |
| Workflow | workflow name | `steps`: ordered `{command, approval, id?, required_outputs?}` |
| Executor | executor name | `default_model` |

A command or workflow that does not exist in `config.toml` can be created from
the UI. A new command needs an `executor`; without a `prompt` the task
instructions are sent unchanged, as with a file command that has no
`prompt_file`. A workflow in the database fully replaces a workflow with the
same name from the file.

Not managed here (still file- or worker-owned): executor argument arrays, model
aliases, repositories, triggers, storage, and server settings.

## Precedence

For each field: **database override > `config.toml` > built-in default**.

Model selection for a step, highest first:

1. The model entered when submitting the task (existing behaviour; applies to every step).
2. The command's `model` override.
3. The executor's `default_model`.
4. Nothing: the worker runs the executor without `{{machinist.model}}`.

Resolution happens when a task is submitted. The resolved commands, prompts,
timeouts, and models are saved with the job exactly as before, so already
submitted tasks keep their snapshot. Retries and revisions reuse the snapshot.

Scheduled triggers and the local `machinist run --command` CLI keep resolving
from the configuration files only. Triggers keep a configuration signature for
their durable state; making them follow database overrides is future work.

## Data model

Two additive tables, created with `CREATE TABLE IF NOT EXISTS` at startup:

```sql
settings_versions(id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT, name TEXT,
  body TEXT NOT NULL,           -- JSON value, or 'null' for "no override"
  reverted_from INTEGER,        -- source version when created by a revert
  created_at TEXT NOT NULL)
worker_executors(worker_instance, executor, models, supports_model)
```

History is append-only. The current value for a `(kind, name)` is its newest
row. Clearing an override appends a `null` row, which restores the file value
(or removes a UI-created command or workflow). Reverting appends a copy of an
earlier row's body, so a revert can itself be reverted. `worker_executors`
records what each worker advertised on its latest poll so the UI can offer, and
the server can validate, only executors and aliases that a worker declared.

`PRAGMA user_version` stays at 5. Older binaries ignore both tables, so a
rollback to the previous release needs no migration; overrides simply stop
applying until the new release is restored. To discard all UI settings, stop
the server and run `DROP TABLE settings_versions;`.

## API

- `GET /api/v1/settings` returns every command, workflow, and executor with
  its file value, current override, effective value, current version, and any
  override that no longer applies (for example, its command was removed from
  `config.toml`).
- `PUT /api/v1/settings/{kind}/{name}` with
  `{"base_version": N, "value": {...} | null}` stores a new version. `kind` is
  `commands`, `workflows`, or `executors`. `base_version` is the version the
  editor loaded (0 for none); a mismatch returns 409 so concurrent editors do
  not overwrite each other.
- `GET /api/v1/settings/{kind}/{name}/history` lists versions, newest first.
- `POST /api/v1/settings/versions/{id}/revert` restores an earlier version.

Mutating endpoints use the same authorization as task submission: a browser
request needs the `X-Machinist-CSRF` token from `/api/v1/status` and a loopback
`Origin`; API clients may use the worker bearer token instead.

## Validation

A change is rejected (400) unless:

- names match `[A-Za-z0-9_-]{1,64}`;
- the executor is advertised by at least one registered worker;
- the model is a single line of at most 128 bytes and, when set, the executor
  supports model selection and at least one worker advertising it lists that
  alias (or declares no aliases);
- the timeout parses as a positive Go duration;
- the prompt is at most 256 KiB and passes the same template checks as a
  `prompt_file`;
- after applying the change, every command and workflow still resolves.

If `config.toml` later changes so a stored override no longer applies, the
server skips that override, keeps serving the file definition, and reports it
in `GET /api/v1/settings` as invalid instead of failing every request.

## Prompt size warning

The prompt editor warns, but does not block, when a prompt exceeds 2 KB or 40
non-empty lines. Small prompts with control flow owned by code are a factory
rule; a large prompt usually means a workflow step should be split. The API
returns the same `warnings` so API clients see them too.

## Security notes

- The UI can only point a command at an executor name a worker advertised;
  the worker still resolves that name against its own allowlist at execution.
- Prompt text is data sent to an allowlisted executor on stdin. It cannot add
  arguments or environment variables.
- The control plane still listens on loopback only. Anyone who can reach the
  UI and read the CSRF token can already submit tasks with arbitrary prompts,
  so settings do not widen who can run what.

## Deferred

- Enabling/disabling executors, editing model aliases, and a smoke **Test**
  button: these touch worker-owned configuration and need a worker-side
  protocol. The executor list is read-only apart from the default model.
- Trigger selections, editing `required_outputs` in the UI (preserved when
  present), and per-worker model validation.
