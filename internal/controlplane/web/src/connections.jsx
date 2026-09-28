import { useCallback, useEffect, useState } from "react";
import { usePolling } from "@/use-polling";
import { Copy, Cpu, ExternalLink, KeyRound, Plug, RefreshCw, Send, Server, X } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { PageHeading, QuietState } from "@/components/ui/page-heading";
import { ExecutorBadge } from "@/executor-badge";
import { connectionStateLabel, connectionTone, connectionsRequest, groupConnectionsByWorker, loginActive, loginKeys, loginResult, loginStep, safeLoginURL } from "@/connections";

const tones = {
  success: "border-success/25 bg-success/10 text-success",
  warning: "border-warning/25 bg-warning/10 text-warning",
  danger: "border-danger/25 bg-danger/10 text-danger",
  muted: "border-border bg-muted text-muted-foreground",
};

const fetchConnections = () => connectionsRequest("/api/v1/connections");
const fetchExecutors = () => connectionsRequest("/api/v1/status");

export function ConnectionsPage({ csrfToken }) {
  const { data, error: loadError, refresh: reload } = usePolling(fetchConnections, 5000);
  const state = { loading: !data && !loadError, error: loadError?.message || "", connections: data?.connections || [] };
  const { data: status, refresh: reloadExecutors } = usePolling(fetchExecutors, 5000);
  const executors = status?.executors || [];
  const [login, setLogin] = useState(null);
  const [startError, setStartError] = useState("");
  const [starting, setStarting] = useState("");

  async function connect(connection, replace = false) {
    const key = `${connection.worker}/${connection.executor}`;
    setStarting(key);
    setStartError("");
    try {
      const result = await connectionsRequest(`/api/v1/connections/${encodeURIComponent(connection.worker)}/${encodeURIComponent(connection.executor)}/login`, { method: "POST", body: { replace }, csrfToken });
      setLogin({ token: result.token, session: result.session });
      await reload();
    } catch (error) {
      setStartError(error.message);
    } finally {
      setStarting("");
    }
  }

  const groups = groupConnectionsByWorker(state.connections);
  return <div className="mx-auto max-w-[1500px] space-y-6 p-4 sm:p-6 lg:p-8">
    <PageHeading title="Connections" description="Log each worker's agent CLIs in to their subscriptions from here. The login runs on the worker and the credentials never leave it." />
    {state.error && <Alert tone="danger">{state.error}</Alert>}
    {startError && <Alert tone="danger">{startError}</Alert>}
    {login && <LoginPanel key={login.session.id} login={login} csrfToken={csrfToken} close={() => { setLogin(null); reload(); }} />}
    {state.loading ? <Card><QuietState title="Preparing the bench" description="Checking each worker's logins." role="status" /></Card> : groups.length ? <div className="max-w-5xl space-y-4">{groups.map((group) => <Card key={group.worker} className="overflow-hidden">
      <header className="flex items-center justify-between gap-3 border-b border-border px-5 py-3">
        <h2 className="flex items-center gap-2 text-sm font-semibold"><Server className="size-4 text-muted-foreground" />{group.worker}</h2>
        <Badge className={group.online ? tones.success : tones.muted}>{group.online ? "Online" : "Offline"}</Badge>
      </header>
      {group.connections.map((connection) => <ConnectionRow key={connection.executor} connection={connection} busy={Boolean(login) || starting !== ""} starting={starting === `${connection.worker}/${connection.executor}`} connect={connect} />)}
    </Card>)}
      <p className="text-xs text-muted-foreground">An executor appears here once its worker declares <code>[executors.&lt;name&gt;.auth]</code> in worker.toml. Runs are not given to an executor whose login has expired.</p>
    </div> : <Card><QuietState title="No connections yet." description="Add an [executors.<name>.auth] recipe to a worker's worker.toml to manage its login here." /></Card>}
    {executors.length > 0 && <ExecutorAvailability executors={executors} csrfToken={csrfToken} reload={reloadExecutors} />}
  </div>;
}

// Rate-limited executors are skipped by the lease query until their reset; Clear ends the stall early.
function ExecutorAvailability({ executors, csrfToken, reload }) {
  return <Card className="max-w-5xl overflow-hidden">
    <header className="border-b border-border px-5 py-3">
      <h2 className="flex items-center gap-2 text-sm font-semibold"><Cpu className="size-4 text-muted-foreground" />Executor availability</h2>
    </header>
    {executors.map((executor) => <article key={executor.id} className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-5 py-3 last:border-b-0">
      <p className="text-sm"><span className="font-medium">{executor.executor}</span><span className="text-muted-foreground"> on {executor.worker}</span></p>
      <ExecutorBadge executor={executor} csrfToken={csrfToken} onCleared={reload} />
    </article>)}
  </Card>;
}

function ConnectionRow({ connection, busy, starting, connect }) {
  const tone = connectionTone(connection.state);
  const inProgress = connection.session && loginActive(connection.session.state);
  return <article className="grid gap-3 border-b border-border px-5 py-4 last:border-b-0 sm:grid-cols-[minmax(10rem,1fr)_minmax(10rem,1.4fr)_auto] sm:items-center">
    <div className="min-w-0">
      <h3 className="flex items-center gap-2 text-sm font-medium"><KeyRound className="size-4 text-muted-foreground" />{connection.executor}</h3>
      <p className="mt-1 text-xs text-muted-foreground">{connection.checked_at ? `Checked ${relativeTime(connection.checked_at)}` : "Not checked yet"}{connection.expires_at ? ` · expires ${new Date(connection.expires_at).toLocaleString()}` : ""}</p>
    </div>
    <div className="min-w-0 space-y-1">
      <Badge className={`gap-1.5 ${tones[tone]}`}><span className="size-1.5 rounded-full bg-current" />{connectionStateLabel(connection.state)}</Badge>
      {connection.detail && <p className="break-words text-xs text-muted-foreground">{connection.detail}</p>}
      {inProgress && <p className="text-xs text-warning">A login is in progress{connection.session.created_at ? `, started ${relativeTime(connection.session.created_at)}` : ""}.</p>}
    </div>
    <div className="flex justify-end gap-2">
      {!connection.login ? <span className="text-xs text-muted-foreground">Status only</span> : inProgress ? <Button className="text-xs!" variant="outline" size="sm" disabled={busy || !connection.online} onClick={() => connect(connection, true)}><RefreshCw className="size-3.5" />Start over</Button> : <Button className="text-xs!" variant={connection.state === "connected" ? "outline" : "default"} size="sm" disabled={busy || !connection.online} onClick={() => connect(connection)}><Plug className="size-3.5" />{starting ? "Starting…" : connection.state === "connected" ? "Reconnect" : "Connect"}</Button>}
    </div>
  </article>;
}

function LoginPanel({ login, csrfToken, close }) {
  const [session, setSession] = useState(login.session);
  const [error, setError] = useState("");
  const [text, setText] = useState("");
  const [sending, setSending] = useState(false);
  const [copied, setCopied] = useState(false);
  const active = loginActive(session.state);
  const base = `/api/v1/connections/sessions/${encodeURIComponent(session.id)}`;

  const fetchSession = useCallback(() => connectionsRequest(base, { loginToken: login.token }), [base, login.token]);
  const { data: polled, error: pollError } = usePolling(active ? fetchSession : null, 1000);
  useEffect(() => {
    if (polled) { setSession(polled); setError(""); }
  }, [polled]);
  useEffect(() => {
    if (pollError) setError(pollError.message);
  }, [pollError]);

  async function send(body) {
    setSending(true);
    setError("");
    try {
      await connectionsRequest(`${base}/input`, { method: "POST", body, csrfToken, loginToken: login.token });
      if (body.text !== undefined) setText("");
    } catch (requestError) {
      setError(requestError.message);
    } finally {
      setSending(false);
    }
  }

  async function cancel() {
    setError("");
    try {
      setSession(await connectionsRequest(`${base}/cancel`, { method: "POST", csrfToken, loginToken: login.token }));
    } catch (requestError) {
      setError(requestError.message);
    }
  }

  async function copyCode() {
    try {
      await navigator.clipboard.writeText(session.code);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  }

  const link = safeLoginURL(session.url);
  const result = loginResult(session);
  return <Card className="max-w-5xl overflow-hidden border-primary/25" aria-live="polite">
    <header className="flex items-center justify-between gap-3 border-b border-border px-5 py-3">
      <h2 className="text-sm font-semibold">Log in {session.executor} on {session.worker}</h2>
      {active ? <Button className="text-xs!" variant="ghost" size="sm" onClick={cancel}><X className="size-3.5" />Cancel login</Button> : <Button className="text-xs!" variant="ghost" size="sm" onClick={close}>Done</Button>}
    </header>
    <div className="space-y-4 p-5 text-sm">
      {error && <Alert tone="danger">{error}</Alert>}
      {active ? <p className="font-medium">{loginStep(session)}</p> : <Alert tone={session.state === "succeeded" ? "success" : "danger"}>{result}</Alert>}
      {active && (link || session.code) && <div className="flex flex-wrap items-center gap-3">
        {link && <Button asChild className="text-xs!" size="sm"><a href={link} target="_blank" rel="noopener noreferrer"><ExternalLink className="size-3.5" />Open login page</a></Button>}
        {session.code && <div className="flex items-center gap-2"><span className="text-xs text-muted-foreground">Code</span><code className="rounded-md border border-border bg-muted px-3 py-1.5 font-mono text-base tracking-widest" aria-label="Device code">{session.code}</code><Button variant="ghost" size="icon" onClick={copyCode} aria-label="Copy device code"><Copy className="size-4" /></Button>{copied && <span className="text-xs text-success">Copied</span>}</div>}
      </div>}
      {active && session.state !== "pending" && <form className="space-y-2" onSubmit={(event) => { event.preventDefault(); send({ text }); }}>
        <label className="block"><span className="field-label">{session.awaiting_input ? "Paste the code here" : "Type into the login"}</span>
          <div className="flex gap-2"><input className={`field-control font-mono ${session.awaiting_input ? "border-primary" : ""}`} value={text} onChange={(event) => setText(event.target.value)} maxLength={4096} autoComplete="off" spellCheck={false} autoFocus={session.awaiting_input} /><Button className="text-xs!" disabled={sending}><Send className="size-3.5" />Send</Button></div>
        </label>
        <div className="flex flex-wrap items-center gap-1" role="group" aria-label="Terminal keys"><span className="mr-1 text-xs text-muted-foreground">Keys for menus:</span>{loginKeys.map((item) => <Button key={item.key} type="button" className="text-xs!" variant="outline" size="sm" disabled={sending} onClick={() => send({ key: item.key })}>{item.label}</Button>)}</div>
        <p className="text-xs text-muted-foreground">What you send goes straight to the login on the worker. It is not stored or logged.</p>
      </form>}
      {session.transcript && <details open={active && !link && !session.code}><summary className="cursor-pointer text-xs text-muted-foreground">Terminal output (links, typed input and tokens are redacted)</summary><pre className="mt-2 max-h-72 overflow-auto whitespace-pre-wrap break-words rounded-md border border-border bg-surface p-3 font-mono text-xs leading-5">{session.transcript}</pre></details>}
    </div>
  </Card>;
}

function Alert({ tone, children }) {
  const classes = { danger: "border-danger/35 bg-danger/10 text-danger", warning: "border-warning/35 bg-warning/10 text-warning", success: "border-success/35 bg-success/10 text-success" };
  return <div role={tone === "danger" ? "alert" : "status"} className={`rounded-md border px-3 py-2 text-sm ${classes[tone]}`}>{children}</div>;
}

function relativeTime(value) { const seconds = Math.max(0, Math.floor((Date.now() - Date.parse(value)) / 1000)); if (seconds < 10) return "just now"; if (seconds < 60) return `${seconds}s ago`; const minutes = Math.floor(seconds / 60); if (minutes < 60) return `${minutes}m ago`; const hours = Math.floor(minutes / 60); if (hours < 24) return `${hours}h ago`; return `${Math.floor(hours / 24)}d ago`; }
