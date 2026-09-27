import { useCallback, useEffect, useState } from "react";
import { ArrowDown, ArrowUp, History, Plus, RotateCcw, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { PageHeading, QuietState } from "@/components/ui/page-heading";
import { Tabs } from "@/components/ui/tabs";
import { commandForm, commandOverride, describeVersion, moveStep, promptWarnings, settingsRequest, sourceLabel, workflowOverride, workflowSteps } from "@/settings";

const namePattern = /^[A-Za-z0-9_-]{1,64}$/;

export function SettingsPage({ csrfToken }) {
  const [state, setState] = useState({ loading: true, error: "", data: null });
  const reload = useCallback(async () => {
    try {
      const data = await settingsRequest("/api/v1/settings");
      setState({ loading: false, error: "", data });
    } catch (error) {
      setState((current) => ({ ...current, loading: false, error: error.message }));
    }
  }, []);
  useEffect(() => { reload(); }, [reload]);
  const data = state.data;
  return <div className="mx-auto max-w-[1500px] space-y-6 p-4 sm:p-6 lg:p-8">
    <PageHeading title="Settings" description="Command defaults, workflows, and executor models. Changes apply to new tasks; submitted tasks keep their saved definitions." />
    {state.error && <Alert tone="danger">{state.error}</Alert>}
    {state.loading ? <Card><QuietState title="Preparing the bench" description="Loading the latest configuration." role="status" /></Card> : data && <div className="max-w-5xl space-y-4">
      {data.problems.length > 0 && <Alert tone="warning"><p className="font-medium">Some saved settings no longer apply, so config.toml is used instead:</p><ul className="mt-1 list-disc pl-5">{data.problems.map((problem) => <li key={`${problem.kind}/${problem.name}`}><code>{problem.kind}/{problem.name}</code>: {problem.error}</li>)}</ul></Alert>}
      <p className="text-xs text-muted-foreground">Executors and model aliases are declared by each worker in worker.toml. Settings can only choose among what workers advertise.</p>
      <Tabs label="Settings sections" items={[
        { id: "commands", label: "Commands", content: <CommandsSettings data={data} csrfToken={csrfToken} reload={reload} /> },
        { id: "workflows", label: "Workflows", content: <WorkflowsSettings data={data} csrfToken={csrfToken} reload={reload} /> },
        { id: "executors", label: "Executors", content: <ExecutorsSettings data={data} csrfToken={csrfToken} reload={reload} /> },
      ]} />
    </div>}
  </div>;
}

function useSave(csrfToken, reload) {
  const [message, setMessage] = useState(null);
  const [saving, setSaving] = useState(false);
  async function save(kind, name, baseVersion, value) {
    setSaving(true);
    setMessage(null);
    try {
      const result = await settingsRequest(`/api/v1/settings/${kind}/${encodeURIComponent(name)}`, { method: "PUT", body: { base_version: baseVersion, value }, csrfToken });
      setMessage({ tone: "success", text: `Saved version ${result.version.id}. New tasks use it.`, warnings: result.warnings || [] });
      await reload();
      return true;
    } catch (error) {
      setMessage({ tone: "danger", text: error.message });
      return false;
    } finally {
      setSaving(false);
    }
  }
  return { message, setMessage, saving, save };
}

function CommandsSettings({ data, csrfToken, reload }) {
  const [selected, setSelected] = useState(data.commands[0]?.name || "");
  const [newName, setNewName] = useState("");
  const view = data.commands.find((command) => command.name === selected) || (selected ? { name: selected, version: 0, file: null, override: null, effective: null } : null);
  return <div className="grid gap-5 md:grid-cols-[14rem_minmax(0,1fr)]">
    <aside className="space-y-3">
      <nav className="grid gap-1" aria-label="Commands">{data.commands.map((command) => <button key={command.name} type="button" onClick={() => setSelected(command.name)} aria-current={selected === command.name ? "true" : undefined} className={`nav-item w-full justify-between ${selected === command.name ? "nav-item-active" : ""}`}><span className="truncate">{command.name}</span>{command.override && <Badge className="border-primary/25 bg-primary/10 text-primary">edited</Badge>}</button>)}</nav>
      <form className="flex gap-2" onSubmit={(event) => { event.preventDefault(); if (namePattern.test(newName)) { setSelected(newName); setNewName(""); } }}>
        <input className="field-control" value={newName} onChange={(event) => setNewName(event.target.value)} placeholder="new-command" aria-label="New command name" pattern="[A-Za-z0-9_-]{1,64}" />
        <Button variant="outline" size="icon" aria-label="Add command"><Plus className="size-4" /></Button>
      </form>
    </aside>
    {view ? <CommandEditor key={`${view.name}:${view.version}`} view={view} executors={data.executors} csrfToken={csrfToken} reload={reload} /> : <Card><QuietState title="No commands configured." description="Add a command name to create one." /></Card>}
  </div>;
}

function CommandEditor({ view, executors, csrfToken, reload }) {
  const [form, setForm] = useState(() => commandForm(view));
  const { message, saving, save } = useSave(csrfToken, reload);
  const update = (field) => (event) => setForm((current) => ({ ...current, [field]: event.target.value }));
  const executorName = form.executor || view.file?.executor || "";
  const executor = executors.find((candidate) => candidate.name === executorName);
  const inheritedPrompt = view.file?.prompt || "";
  const warnings = promptWarnings(form.prompt || inheritedPrompt);
  const advertised = executors.filter((candidate) => candidate.workers.length > 0);
  return <Card className="space-y-5 p-5">
    <header className="flex flex-wrap items-center justify-between gap-2">
      <div><h2 className="text-sm font-semibold">{view.name}</h2><p className="mt-1 text-xs text-muted-foreground">{view.effective ? `${view.effective.executor}${view.effective.model ? ` · ${view.effective.model}` : ""} · ${view.effective.timeout}` : "Not saved yet"}</p></div>
      <Badge className="border-border bg-muted text-muted-foreground">{sourceLabel(view)}</Badge>
    </header>
    {view.error && <Alert tone="danger">{view.error}</Alert>}
    <div className="grid gap-4 sm:grid-cols-3">
      <label><span className="field-label">Executor</span><select className="field-control" value={form.executor} onChange={update("executor")}>
        <option value="">{view.file ? `From config.toml (${view.file.executor})` : "Choose an executor"}</option>
        {advertised.map((candidate) => <option key={candidate.name} value={candidate.name}>{candidate.name} · {candidate.workers.join(", ")}</option>)}
      </select></label>
      <label><span className="field-label">Default model · optional</span><input className="field-control" list={`models-${view.name}`} value={form.model} onChange={update("model")} maxLength={128} placeholder={executor?.default_model ? `Executor default (${executor.default_model})` : "Worker default"} disabled={executor ? !executor.supports_model : false} />
        <datalist id={`models-${view.name}`}>{executor?.models.map((model) => <option key={model} value={model} />)}</datalist></label>
      <label><span className="field-label">Timeout · optional</span><input className="field-control" value={form.timeout} onChange={update("timeout")} placeholder={view.file?.timeout || "30m0s"} /></label>
    </div>
    <label className="block space-y-2">
      <span className="flex items-center justify-between gap-2"><span className="field-label mb-0">Prompt</span>{!form.prompt && view.file?.prompt_file && <Button type="button" variant="ghost" size="sm" onClick={() => setForm((current) => ({ ...current, prompt: inheritedPrompt }))}>Edit a copy of the file prompt</Button>}</span>
      <textarea className="field-control min-h-56 resize-y font-mono text-xs" value={form.prompt} onChange={update("prompt")} placeholder={inheritedPrompt || "Leave empty to send the task instructions unchanged. Use {{task.spec}}, {{task.output_dir}}, or {{machinist.prompt}}."} />
      <span className="block text-xs text-muted-foreground">{form.prompt ? "This prompt replaces the command's prompt_file for new tasks." : view.file?.prompt_file ? `Empty uses ${view.file.prompt_file}.` : "Empty uses the task instructions directly."}</span>
    </label>
    {warnings.length > 0 && <Alert tone="warning">{warnings.map((warning) => <p key={warning}>{warning}</p>)}<p>Consider splitting this into smaller workflow steps.</p></Alert>}
    <SaveMessage message={message} />
    <div className="flex flex-wrap justify-end gap-2">
      {view.override && <Button type="button" variant="outline" disabled={saving} onClick={() => save("commands", view.name, view.version, null)}><Trash2 className="size-4" />{view.file ? "Use config.toml" : "Delete command"}</Button>}
      <Button type="button" disabled={saving} onClick={() => save("commands", view.name, view.version, commandOverride(form))}>{saving ? "Saving…" : "Save"}</Button>
    </div>
    {view.version > 0 && <VersionHistory kind="commands" name={view.name} current={view.version} csrfToken={csrfToken} reload={reload} />}
  </Card>;
}

function WorkflowsSettings({ data, csrfToken, reload }) {
  const [selected, setSelected] = useState(data.workflows[0]?.name || "");
  const [newName, setNewName] = useState("");
  const view = data.workflows.find((workflow) => workflow.name === selected) || (selected ? { name: selected, version: 0, file: null, override: null, effective: null } : null);
  return <div className="space-y-4">
    <div className="flex flex-wrap items-end gap-3">
      <label className="block w-full max-w-xs"><span className="field-label">Workflow</span><select className="field-control" value={selected} onChange={(event) => setSelected(event.target.value)}>{!data.workflows.some((workflow) => workflow.name === selected) && selected && <option value={selected}>{selected} (new)</option>}{data.workflows.map((workflow) => <option key={workflow.name} value={workflow.name}>{workflow.name}{workflow.override ? " · edited" : ""}</option>)}</select></label>
      <form className="flex gap-2" onSubmit={(event) => { event.preventDefault(); if (namePattern.test(newName)) { setSelected(newName); setNewName(""); } }}>
        <input className="field-control" value={newName} onChange={(event) => setNewName(event.target.value)} placeholder="new-workflow" aria-label="New workflow name" pattern="[A-Za-z0-9_-]{1,64}" />
        <Button variant="outline"><Plus className="size-4" />New</Button>
      </form>
    </div>
    {view ? <WorkflowEditor key={`${view.name}:${view.version}`} view={view} commands={data.commands} csrfToken={csrfToken} reload={reload} /> : <Card><QuietState title="No workflows configured." description="Name a new workflow to create one." /></Card>}
  </div>;
}

function WorkflowEditor({ view, commands, csrfToken, reload }) {
  const [steps, setSteps] = useState(() => workflowSteps(view));
  const { message, saving, save } = useSave(csrfToken, reload);
  const setStep = (index, patch) => setSteps((current) => current.map((step, position) => position === index ? { ...step, ...patch } : step));
  return <Card className="space-y-4 p-5">
    <header className="flex flex-wrap items-center justify-between gap-2"><h2 className="text-sm font-semibold">{view.name}</h2><Badge className="border-border bg-muted text-muted-foreground">{view.version ? sourceLabel(view) : "New"}</Badge></header>
    {view.error && <Alert tone="danger">{view.error}</Alert>}
    <ol className="space-y-2">{steps.map((step, index) => <li key={index} className="flex flex-wrap items-center gap-2 rounded-md border border-border p-3">
      <span className="flex size-7 shrink-0 items-center justify-center rounded-full bg-muted text-xs text-muted-foreground">{index + 1}</span>
      <select className="field-control w-auto min-w-40 flex-1" aria-label={`Step ${index + 1} command`} value={step.command} onChange={(event) => setStep(index, { command: event.target.value })}>{!commands.some((command) => command.name === step.command) && <option value={step.command}>{step.command || "Choose a command"}</option>}{commands.map((command) => <option key={command.name} value={command.name}>{command.name}</option>)}</select>
      <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={step.approval} onChange={(event) => setStep(index, { approval: event.target.checked })} />Approval before</label>
      <Button type="button" variant="ghost" size="icon" aria-label={`Move step ${index + 1} up`} onClick={() => setSteps((current) => moveStep(current, index, -1))}><ArrowUp className="size-4" /></Button>
      <Button type="button" variant="ghost" size="icon" aria-label={`Move step ${index + 1} down`} onClick={() => setSteps((current) => moveStep(current, index, 1))}><ArrowDown className="size-4" /></Button>
      <Button type="button" variant="ghost" size="icon" aria-label={`Remove step ${index + 1}`} onClick={() => setSteps((current) => current.filter((_, position) => position !== index))}><Trash2 className="size-4" /></Button>
    </li>)}</ol>
    <Button type="button" variant="outline" size="sm" disabled={steps.length >= 32} onClick={() => setSteps((current) => [...current, { command: commands[0]?.name || "", approval: false }])}><Plus className="size-3.5" />Add step</Button>
    <SaveMessage message={message} />
    <div className="flex flex-wrap justify-end gap-2">
      {view.override && <Button type="button" variant="outline" disabled={saving} onClick={() => save("workflows", view.name, view.version, null)}><Trash2 className="size-4" />{view.file ? "Use config.toml" : "Delete workflow"}</Button>}
      <Button type="button" disabled={saving || !steps.length} onClick={() => save("workflows", view.name, view.version, workflowOverride(steps))}>{saving ? "Saving…" : "Save"}</Button>
    </div>
    {view.version > 0 && <VersionHistory kind="workflows" name={view.name} current={view.version} csrfToken={csrfToken} reload={reload} />}
  </Card>;
}

function ExecutorsSettings({ data, csrfToken, reload }) {
  return data.executors.length ? <Card className="overflow-hidden">{data.executors.map((executor) => <ExecutorRow key={`${executor.name}:${executor.version}`} executor={executor} csrfToken={csrfToken} reload={reload} />)}</Card> : <Card><QuietState title="No executors advertised." description="Start a worker; its worker.toml executors will appear here." /></Card>;
}

function ExecutorRow({ executor, csrfToken, reload }) {
  const [model, setModel] = useState(executor.override?.default_model || "");
  const [showHistory, setShowHistory] = useState(false);
  const { message, saving, save } = useSave(csrfToken, reload);
  const offline = executor.workers.length === 0;
  return <article className="space-y-3 border-b border-border p-4 last:border-b-0 sm:px-5">
    <div className="grid gap-3 sm:grid-cols-[minmax(10rem,1fr)_minmax(12rem,1fr)_auto] sm:items-end">
      <div className="min-w-0"><h2 className="font-mono text-sm font-medium">{executor.name}</h2><p className="mt-1 text-xs text-muted-foreground">{offline ? "Not advertised by any registered worker" : `Workers: ${executor.workers.join(", ")}`}</p><p className="mt-1 text-xs text-muted-foreground">{executor.supports_model ? `Models: ${executor.models.length ? executor.models.join(", ") : "any"}` : "No model selection"}</p></div>
      <label><span className="field-label">Default model</span>{executor.any_model || !executor.models.length ? <input className="field-control" value={model} onChange={(event) => setModel(event.target.value)} maxLength={128} placeholder="Worker default" disabled={!executor.supports_model || offline} /> : <select className="field-control" value={model} onChange={(event) => setModel(event.target.value)} disabled={offline}><option value="">Worker default</option>{executor.models.map((alias) => <option key={alias} value={alias}>{alias}</option>)}</select>}</label>
      <div className="flex gap-2">
        <Button type="button" disabled={saving || offline || !executor.supports_model} onClick={() => save("executors", executor.name, executor.version, model.trim() ? { default_model: model.trim() } : null)}>Save</Button>
        {executor.version > 0 && <Button type="button" variant="ghost" size="icon" aria-label={`History for ${executor.name}`} aria-expanded={showHistory} onClick={() => setShowHistory((value) => !value)}><History className="size-4" /></Button>}
      </div>
    </div>
    <SaveMessage message={message} />
    {showHistory && <VersionHistory kind="executors" name={executor.name} current={executor.version} csrfToken={csrfToken} reload={reload} />}
  </article>;
}

function VersionHistory({ kind, name, current, csrfToken, reload }) {
  const [versions, setVersions] = useState([]);
  const [error, setError] = useState("");
  const [reverting, setReverting] = useState(0);
  useEffect(() => {
    let active = true;
    settingsRequest(`/api/v1/settings/${kind}/${encodeURIComponent(name)}/history`).then((result) => { if (active) setVersions(result.versions); }).catch((requestError) => { if (active) setError(requestError.message); });
    return () => { active = false; };
  }, [kind, name, current]);
  async function revert(id) {
    setReverting(id);
    setError("");
    try {
      await settingsRequest(`/api/v1/settings/versions/${id}/revert`, { method: "POST", csrfToken });
      await reload();
    } catch (requestError) {
      setError(requestError.message);
    } finally {
      setReverting(0);
    }
  }
  return <section className="space-y-2 border-t border-border pt-4" aria-label={`${name} history`}>
    <h3 className="flex items-center gap-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground"><History className="size-3.5" />History</h3>
    {error && <Alert tone="danger">{error}</Alert>}
    <ol className="space-y-1">{versions.map((version) => <li key={version.id} className="flex flex-wrap items-center justify-between gap-2 text-xs">
      <span><span className="font-mono">v{version.id}</span> · {describeVersion(version)} · <time dateTime={version.created_at}>{new Date(version.created_at).toLocaleString()}</time></span>
      {version.id === current ? <Badge className="border-success/25 bg-success/10 text-success">Current</Badge> : <Button type="button" variant="ghost" size="sm" disabled={reverting !== 0} onClick={() => revert(version.id)}><RotateCcw className="size-3.5" />{reverting === version.id ? "Reverting…" : "Revert"}</Button>}
    </li>)}</ol>
  </section>;
}

function SaveMessage({ message }) {
  if (!message) return null;
  return <Alert tone={message.tone}>{message.text}{message.warnings?.map((warning) => <p key={warning}>{warning}</p>)}</Alert>;
}

function Alert({ tone, children }) {
  const tones = { danger: "border-danger/35 bg-danger/10 text-danger", warning: "border-warning/35 bg-warning/10 text-warning", success: "border-success/35 bg-success/10 text-success" };
  return <div role={tone === "danger" ? "alert" : "status"} className={`rounded-md border px-3 py-2 text-sm ${tones[tone]}`}>{children}</div>;
}
