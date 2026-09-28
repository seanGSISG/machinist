import { TaskDetail } from "./task-detail.jsx";
import { RunDetail } from "./run-detail.jsx";
import { GatePanel } from "./gate-panel.jsx";
import { UsagePage } from "./usage.jsx";
import { State, friendlyName, relativeTime } from "./task-display.jsx";
import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import "@fontsource-variable/manrope";
import { Activity, BarChart3, Bot, CircleAlert, GitBranch, KeyRound, LayoutDashboard, Moon, Play, Plug, Plus, Server, Settings, Sun, Table2, TimerReset, X } from "lucide-react";
import { Analytics } from "@/analytics";
import { CommandsPage, WorkersPage } from "@/catalog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { PageHeading } from "@/components/ui/page-heading";
import { cn } from "@/lib/utils";
import { routeFromHash } from "@/routes";
import { boardColumns, capFinished, currentRun, filterJobs, groupJobsByBoardColumn, jobCounts, jobDisplayTitle } from "@/runs-board";
import { groupJobs, visibleJobs } from "@/ticket-board";
import { ConnectionsPage } from "@/connections.jsx";
import { attentionTone, needsMe, relativeAge, titleFor } from "@/needs-me";
import { SettingsPage } from "@/settings.jsx";
import { createStatusLoader } from "@/status-loader";
import { TriggersPage } from "@/triggers";
import { usePolling } from "@/use-polling";
import "./styles.css";


function App() {
  const [status, setStatus] = useState({ jobs: [], workers: [], commands: [], repositories: [], triggers: [], connections: [], gates_awaiting_approval: [], blocked_jobs: [], logins: [], csrf_token: "" });
  const [selection, setSelection] = useState("");
  const [repository, setRepository] = useState("");
  const [prompt, setPrompt] = useState("");
 const [title,setTitle]=useState("");
 const [sourceURL,setSourceURL]=useState("");
  const [model, setModel] = useState("");
  const [statusError, setStatusError] = useState("");
  const [statusLoaded, setStatusLoaded] = useState(false);
  const [submitError, setSubmitError] = useState("");
  const [taskActionError, setTaskActionError] = useState("");
  const [deletingJob, setDeletingJob] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [composerOpen, setComposerOpen] = useState(false);
  const [filter, setFilter] = useState("all");
  const [runsView, setRunsView] = useState("board");
  const [groupBy, setGroupBy] = useState("none");
  const [showSuperseded, setShowSuperseded] = useState(false);
  const [dark, setDark] = useState(() => localStorage.getItem("machinist-theme") !== "light");
  const [route, setRoute] = useState(() => routeFromHash(window.location.hash));
  const view = route.view;
  const statusLoader = useRef(null);
  if (!statusLoader.current) statusLoader.current = createStatusLoader({
    apply: (result) => {
      if (result.kind === "error") {
        setStatusError(result.message);
        return;
      }
      const next = result.status;
      setStatus(next);
      setStatusError("");
      setStatusLoaded(true);
      const available = new Set(selectionChoices(next).map((choice) => choice.value));
      setSelection((current) => available.has(current) ? current : available.has(localStorage.getItem("machinist-workflow")) ? localStorage.getItem("machinist-workflow") : firstSelection(next));
      const availableRepositories = next.repositories || [];
      setRepository((current) => availableRepositories.includes(current) ? current : availableRepositories.includes(localStorage.getItem("machinist-repository")) ? localStorage.getItem("machinist-repository") : availableRepositories[0] || "");
    },
  });

  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark);
    localStorage.setItem("machinist-theme", dark ? "dark" : "light");
  }, [dark]);

  const attentionItems = useMemo(() => needsMe(status), [status]);
  useEffect(() => {
    document.title = titleFor(attentionItems.length);
  }, [attentionItems.length]);

  useEffect(() => {
    const updateView = () => {
      setTaskActionError("");
      setRoute(routeFromHash(window.location.hash));
    };
    window.addEventListener("hashchange", updateView);
    return () => window.removeEventListener("hashchange", updateView);
  }, []);

  const loadStatus = useCallback(() => statusLoader.current.refresh(), []);
  const { refresh: refreshStatus } = usePolling(loadStatus, 2000);
  useEffect(() => () => statusLoader.current.cancel(), []);

  const choices = useMemo(() => selectionChoices(status), [status.commands, status.workflows]);

  const repositories = status.repositories;

  const shownJobs = useMemo(() => visibleJobs(status.jobs, { showSuperseded }), [showSuperseded, status.jobs]);
  const counts = useMemo(() => jobCounts(shownJobs), [shownJobs]);
  const filteredJobs = useMemo(() => filter === "needs_me" ? shownJobs.filter((job) => job.attention_reason) : filterJobs(shownJobs, filter), [filter, shownJobs]);
  const jobGroups = useMemo(() => groupJobs(filteredJobs, groupBy), [filteredJobs, groupBy]);

  const connectedWorkers = status.workers.filter((worker) => worker.connected).length;
  const loginAttention = status.logins || [];
  const selectedJob = route.jobID ? status.jobs.find((job) => job.id === route.jobID) : undefined;

  async function submit(event) {
    event.preventDefault();
    setSubmitting(true);
    setSubmitError("");
    try {
      const response = await fetch("/api/v1/jobs", {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-Machinist-CSRF": status.csrf_token },
        body: JSON.stringify({ repository, model: model.trim(), ...(selection.startsWith("workflow:") ? { workflow: selection.slice(9),title: title || prompt.trim().split("\n")[0].slice(0,100),source_url:sourceURL || (/^https?:\/\/\S+$/.test(prompt.trim()) ? prompt.trim() : ""),spec:/^https?:\/\/\S+$/.test(prompt.trim()) ? "" : prompt } : { command: selection.slice(8),prompt }) }),
      });
      if (!response.ok) {
        const body = await response.json().catch(() => ({}));
        throw new Error(body.error || `Submission failed (${response.status})`);
      }
      localStorage.setItem("machinist-workflow",selection); localStorage.setItem("machinist-repository",repository);
      const created = await response.json();
      setPrompt(""); setTitle(""); setSourceURL("");
      setComposerOpen(false);
      await refreshStatus();
      window.location.hash = `#/runs/${created.id}`;
    } catch (requestError) {
      setSubmitError(requestError.message);
    } finally {
      setSubmitting(false);
    }
  }

  async function workflowAction(job, action, stopped = false, feedback = "") {
    setTaskActionError("");
    try {
      const response = await fetch(`/api/v1/jobs/${encodeURIComponent(job.id)}/${action}`, {
        method: "POST", headers: { "Content-Type": "application/json", "X-Machinist-CSRF": status.csrf_token },
        body: JSON.stringify({ run_id: job.runs.at(-1)?.id, previous_process_stopped: stopped, feedback }),
      });
      if (!response.ok) { const body = await response.json().catch(() => ({})); throw new Error(body.error || "Unable to update job"); }
      await refreshStatus();
    } catch (error) { setTaskActionError(error.message); }
  }

  async function deleteJob(job) {
    if (!window.confirm(`Delete task ${shortId(job.id)} and all of its stored run data?`)) return;
    setDeletingJob(job.id);
    setTaskActionError("");
    try {
      const response = await fetch(`/api/v1/jobs/${encodeURIComponent(job.id)}`, {
        method: "DELETE",
        headers: { "X-Machinist-CSRF": status.csrf_token },
      });
      if (!response.ok) {
        const body = await response.json().catch(() => ({}));
        throw new Error(body.error || `Delete failed (${response.status})`);
      }
      await refreshStatus();
      window.location.hash = "#/runs";
    } catch (requestError) {
      setTaskActionError(requestError.message);
    } finally {
      setDeletingJob("");
    }
  }

  return (
    <div className="app-shell min-h-dvh bg-background text-foreground md:flex">
      <aside className="app-sidebar sticky top-0 z-20 flex shrink-0 items-center border-b border-border bg-sidebar px-3 py-2 md:h-screen md:w-56 md:flex-col md:items-stretch md:border-b-0 md:border-r md:px-4 md:py-5">
        <div className="brand-lockup flex h-10 items-center gap-3 px-1">
          <MachinistMark />
          <span className="brand-wordmark">machinist</span>
        </div>
        <nav className="ml-4 flex flex-1 gap-1 overflow-x-auto max-md:pb-[max(0.4rem,env(safe-area-inset-bottom))] md:ml-0 md:mt-9 md:block md:overflow-visible" aria-label="Primary">
          <a href="#/runs" aria-current={view === "runs" || view === "task" ? "page" : undefined} className={cn("nav-item", (view === "runs" || view === "task") && "nav-item-active")}><Activity className="size-4" /><span>Tasks</span><span className="ml-auto text-xs text-muted-foreground">{counts.all}</span></a>
          <a href="#/analytics" aria-current={view === "analytics" ? "page" : undefined} className={cn("nav-item", view === "analytics" && "nav-item-active")}><BarChart3 className="size-4" /><span>Analytics</span></a>
          <a href="#/usage" aria-current={view === "usage" ? "page" : undefined} className={cn("nav-item", view === "usage" && "nav-item-active")}><BarChart3 className="size-4" /><span>Usage</span></a>
          <a href="#/workers" aria-current={view === "workers" ? "page" : undefined} className={cn("nav-item", view === "workers" && "nav-item-active")}><Server className="size-4" /><span>Workers</span></a>
          <a href="#/triggers" aria-current={view === "triggers" ? "page" : undefined} className={cn("nav-item", view === "triggers" && "nav-item-active")}><TimerReset className="size-4" /><span>Triggers</span><span className="ml-auto text-xs text-muted-foreground">{status.triggers?.length || 0}</span></a>
          <a href="#/workflows" aria-current={["commands", "workflows"].includes(view) ? "page" : undefined} className={cn("nav-item", ["commands", "workflows"].includes(view) && "nav-item-active")}><Bot className="size-4" /><span>Workflows</span></a>
          <a href="#/connections" aria-current={view === "connections" ? "page" : undefined} className={cn("nav-item", view === "connections" && "nav-item-active")}><Plug className="size-4" /><span>Connections</span>{loginAttention.length > 0 && <span className="ml-auto text-xs text-danger">{loginAttention.length}</span>}</a>
          <a href="#/settings" aria-current={view === "settings" ? "page" : undefined} className={cn("nav-item", view === "settings" && "nav-item-active")}><Settings className="size-4" /><span>Settings</span></a>
        </nav>
        <div className="hidden border-t border-border pt-3 md:block">
          <div className="nav-item" title={`${connectedWorkers} connected · ${status.workers.length} registered`}><Server className="size-4 shrink-0" /><span className="min-w-0 truncate whitespace-nowrap">{connectedWorkers ? `${connectedWorkers} worker${connectedWorkers === 1 ? "" : "s"} online` : "No workers online"}</span></div>
          <button onClick={() => setDark((value) => !value)} className="nav-item w-full" aria-label={`Switch to ${dark ? "light" : "dark"} theme`}>
            {dark ? <Moon className="size-4" /> : <Sun className="size-4" />}<span>{dark ? "Dark" : "Light"} theme</span>
          </button>
        </div>
        <button onClick={() => setDark((value) => !value)} className="mobile-theme ml-auto grid size-9 place-items-center text-muted-foreground md:hidden" aria-label={`Switch to ${dark ? "light" : "dark"} theme`}>
          {dark ? <Moon className="size-4" /> : <Sun className="size-4" />}
        </button>
      </aside>

      <main className="workshop min-w-0 flex-1">
        {view === "task" ? <TaskDetail csrfToken={status.csrf_token} jobID={route.jobID} summary={selectedJob} error={statusError || taskActionError} deleting={deletingJob === route.jobID} onDelete={deleteJob} onWorkflowAction={workflowAction} /> : view === "run" ? <RunDetail runID={route.runID} /> : view === "gate" ? <GatePanel jobID={route.jobID} /> : view === "usage" ? <UsagePage /> : view === "analytics" ? <Analytics jobs={status.jobs} loaded={statusLoaded} error={statusError} /> : view === "workers" ? <WorkersPage workers={status.workers} loaded={statusLoaded} error={statusError} /> : view === "triggers" ? <TriggersPage triggers={status.triggers || []} loaded={statusLoaded} error={statusError} /> : ["commands", "workflows"].includes(view) ? <CommandsPage /> : view === "settings" ? <SettingsPage csrfToken={status.csrf_token} /> : view === "connections" ? <ConnectionsPage csrfToken={status.csrf_token} /> : <div className="mx-auto max-w-[1500px] space-y-6 p-4 sm:p-6 lg:p-8">
          <PageHeading title="Tasks" description="Describe the work. Review the result.">
            <div className="flex items-center gap-2">
              <Button className="text-xs!" onClick={() => setComposerOpen(true)}><Plus className="size-4" />New task</Button>
            </div>
          </PageHeading>

          {composerOpen && <RunComposer title={title} setTitle={setTitle} sourceURL={sourceURL} setSourceURL={setSourceURL} choices={choices} repositories={repositories} selection={selection} setSelection={setSelection} repository={repository} setRepository={setRepository} prompt={prompt} setPrompt={setPrompt} model={model} setModel={setModel} submitting={submitting} submit={submit} close={() => setComposerOpen(false)} />}
          {(statusError || submitError) && <div role="alert" className="rounded-md border border-danger/35 bg-danger/10 px-3 py-2 text-sm text-danger">{submitError || statusError}</div>}

          <section>
            <div className="mb-3 flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
              <div className="flex flex-wrap items-center gap-1" role="group" aria-label="Filter runs">
                {[["all", "All"], ["needs_me", "Needs me"], ["active", "Active"], ["failed", "Failed"], ["succeeded", "Succeeded"]].map(([value, label]) => (
                  <Button key={value} variant="ghost" size="sm" aria-pressed={filter === value} onClick={() => setFilter(value)} className={cn("text-xs!", filter === value && "bg-muted text-foreground")}>{label}<span className="text-muted-foreground">{value === "needs_me" ? attentionItems.length : counts[value]}</span></Button>
                ))}
              </div>
              <div className="flex flex-wrap items-center justify-between gap-3 lg:justify-end">
                <p className="text-xs text-muted-foreground">{counts.all} task{counts.all === 1 ? "" : "s"}</p>
                <Button variant="ghost" size="sm" className={cn("h-7 px-2.5 text-xs!", showSuperseded && "bg-muted text-foreground")} aria-pressed={showSuperseded} onClick={() => setShowSuperseded((value) => !value)}>Show superseded</Button>
                <div className="inline-flex items-center rounded-lg bg-muted/60 p-1" role="group" aria-label="Group runs">
                  <span className="px-1.5 text-xs text-muted-foreground">Group</span>
                  {[["none", "None"], ["ticket", "Ticket"]].map(([value, label]) => <Button key={value} variant="ghost" size="sm" className={cn("h-7 border-transparent px-2.5 text-xs!", groupBy === value && "bg-surface text-foreground shadow-xs")} aria-pressed={groupBy === value} onClick={() => setGroupBy(value)}>{label}</Button>)}
                </div>
                <div className="inline-flex rounded-lg bg-muted/60 p-1" role="group" aria-label="Runs view">
                  <Button variant="ghost" size="sm" className={cn("h-7 border-transparent px-2.5 text-xs!", runsView === "board" && "bg-surface text-foreground shadow-xs")} aria-pressed={runsView === "board"} onClick={() => setRunsView("board")}><LayoutDashboard className="size-3.5" />Board</Button>
                  <Button variant="ghost" size="sm" className={cn("h-7 border-transparent px-2.5 text-xs!", runsView === "table" && "bg-surface text-foreground shadow-xs")} aria-pressed={runsView === "table"} onClick={() => setRunsView("table")}><Table2 className="size-3.5" />List</Button>
                </div>
              </div>
            </div>

            {!jobGroups.length ? runsView === "board" ? <RunBoard jobs={[]} attentionItems={loginAttention} finishedLimit={status.finished_limit} /> : loginAttention.length ? <div className="grid gap-2">{loginAttention.map((item) => <AttentionCard key={item.id} item={item} />)}</div> : <Card className="overflow-hidden"><EmptyRuns filtered={filter !== "all"} openComposer={() => setComposerOpen(true)} /></Card> : <div className="space-y-6">
              {jobGroups.map((group, index) => <section key={group.key} aria-label={group.label || undefined}>
                {group.label && <h3 className="sticky top-0 z-10 -mx-1 mb-2 flex items-center justify-between gap-3 bg-background/95 px-1 py-2 text-sm font-semibold backdrop-blur max-md:top-14"><span className="min-w-0 truncate">{group.label}</span><span className="shrink-0 text-xs font-normal text-muted-foreground">{group.jobs.length} task{group.jobs.length === 1 ? "" : "s"}</span></h3>}
                {runsView === "board" ? <RunBoard jobs={group.jobs} attentionItems={index === 0 ? loginAttention : []} finishedLimit={status.finished_limit} idPrefix={group.key} /> : <div className="space-y-2">{index === 0 && loginAttention.map((item) => <AttentionCard key={item.id} item={item} />)}<Card className="overflow-hidden">{group.jobs.map((job) => <RunRow key={job.id} job={job} />)}</Card></div>}
              </section>)}
            </div>}
          </section>

        </div>}
      </main>
    </div>
  );
}

function RunComposer({ title,setTitle,sourceURL,setSourceURL,choices,repositories,selection,setSelection,repository,setRepository,prompt,setPrompt,model,setModel,submitting,submit,close }) {
  const specHintID=React.useId();
  const isTask=selection.startsWith("workflow:");
  return <Card className="overflow-hidden border-primary/25">
    <div className="flex items-center justify-between border-b border-border px-5 py-3"><h2 className="text-sm font-semibold">New task</h2><Button variant="ghost" size="icon" onClick={close} aria-label="Close new task form"><X className="size-4" /></Button></div>
    <form onSubmit={submit} className="space-y-5 p-5">
      <label className="block space-y-2"><span className="text-sm font-medium">What do you want done?</span><textarea autoFocus aria-describedby={specHintID} className="field-control min-h-32 resize-y" value={prompt} onChange={e=>setPrompt(e.target.value)} placeholder="Describe a change or paste an issue link…" required={!sourceURL.trim() || !isTask} /></label>
      <p id={specHintID} className="text-xs leading-5 text-muted-foreground">{isTask ? <>This is your task’s spec: describe what to build and what counts as done. Prompt templates reference it as <code>{"{{task.spec}}"}</code>. A link on its own is saved as the source instead. <a href="#/workflows" className="text-primary underline">Template variables</a></> : "Write the instructions for this command. Its prompt template receives them as {{machinist.prompt}}."}</p>
      <section className="text-sm">
        <div className="grid gap-4 sm:grid-cols-2">
          <label><span className="field-label">{isTask ? "Workflow" : "Command"}</span><select className="field-control" value={selection} onChange={e=>setSelection(e.target.value)} required>{choices.map(c=><option key={c.value} value={c.value}>{c.label}</option>)}</select></label>
          <label><span className="field-label">Repository</span><select className="field-control" value={repository} onChange={e=>setRepository(e.target.value)} required>{!repositories.length && <option value="">No repositories available</option>}{repositories.map(r=><option key={r} value={r}>{r}</option>)}</select></label>
          {isTask && <><label><span className="field-label">Title · optional</span><input className="field-control" value={title} onChange={e=>setTitle(e.target.value)} maxLength={512} placeholder="From your instructions by default" /><span className="mt-1 block text-xs text-muted-foreground">Template: <code>{"{{task.title}}"}</code></span></label><label><span className="field-label">Source link · optional</span><input type="url" className="field-control" value={sourceURL} onChange={e=>setSourceURL(e.target.value)} placeholder="https://github.com/…" /><span className="mt-1 block text-xs text-muted-foreground">Template: <code>{"{{task.source_url}}"}</code></span></label></>}
          <label><span className="field-label">Model · optional</span><input className="field-control" value={model} onChange={e=>setModel(e.target.value)} maxLength={128} placeholder="Workflow default" /></label>
        </div>
      </section>
      {!choices.length && <p role="alert" className="text-sm text-danger">Configure a workflow before starting a task.</p>}
      <div className="flex justify-end"><Button disabled={submitting || !selection || !repository}>{submitting ? "Starting…" : "Start task"}<Play className="size-3.5" /></Button></div>
    </form>
  </Card>;
}

function RunBoard({ jobs, attentionItems, finishedLimit, idPrefix = "all" }) {
  const groupedJobs = groupJobsByBoardColumn(jobs);
  const finished = capFinished(groupedJobs.finished, finishedLimit);
  const shownJobs = (column) => column === "finished" ? finished.shown : groupedJobs[column];
  const extra = (column) => column === "attention" ? attentionItems.length : 0;
  const headingID = (column) => `board-${idPrefix}-${column}`.replace(/[^\w-]/g, "_");
  return <div className="grid min-w-0 gap-4 md:grid-cols-2 xl:grid-cols-4">
    {boardColumns.map((column) => <section key={column.id} className="run-column min-w-0 border border-border bg-muted/20" aria-labelledby={headingID(column.id)}>
      <header className="flex items-center justify-between gap-3 border-b border-border px-3 py-2.5">
        <div className="min-w-0"><h2 id={headingID(column.id)} className="text-sm font-semibold">{column.title}</h2><p className="break-words text-xs text-muted-foreground">{column.description}</p></div>
        <Badge className="shrink-0 border-border bg-surface text-muted-foreground" aria-label={`${groupedJobs[column.id].length + extra(column.id)} visible ${column.title.toLowerCase()} items`}>{groupedJobs[column.id].length + extra(column.id)}</Badge>
      </header>
      <div className="grid min-w-0 gap-2 p-2">
        {column.id === "attention" && attentionItems.map((item) => <AttentionCard key={item.id} item={item} />)}
        {groupedJobs[column.id].length ? shownJobs(column.id).map((job) => <RunCard key={job.id} job={job} />) : !extra(column.id) && <p className="px-2 py-8 text-center text-xs text-muted-foreground">No runs</p>}
        {column.id === "finished" && finished.hidden > 0 && <p className="px-2 py-2 text-center text-xs text-muted-foreground">{finished.hidden} more</p>}
      </div>
    </section>)}
  </div>;
}

function RunCard({ job }) {
  const title = jobDisplayTitle(job);
  const run = currentRun(job);
  const href = job.attention_reason === "GateAwaitingApproval" ? `#/gate/${encodeURIComponent(job.id)}` : `#/runs/${encodeURIComponent(job.id)}`;
  return <Card className="overflow-hidden"><a href={href} className="block min-w-0 space-y-3 p-4 transition hover:bg-muted/35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/50" aria-label={`Open task ${title}`}>
    <p className="line-clamp-2 text-sm font-medium leading-5">{title}</p>
    <p className="text-xs text-muted-foreground">{job.repository} · {friendlyName(run?.command || job.command)}</p>
    {job.attention_reason ? <AttentionTone reason={job.attention_reason} since={job.waiting_since} /> : <State value={job.state} />}
  </a></Card>;
}

function AttentionCard({ item }) {
  return <Card className="overflow-hidden border-danger/35"><a href={item.html_url} className="block min-w-0 space-y-2 p-4 transition hover:bg-muted/35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/50" aria-label={item.message}>
    <AttentionTone reason={item.reason} since={item.since} />
    <p className="text-xs text-muted-foreground">{item.message}</p>
  </a></Card>;
}

function AttentionTone({ reason, since }) {
  const tone = attentionTone(reason);
  const Icon = tone.icon === "login" ? KeyRound : CircleAlert;
  return <p className="flex items-center gap-1.5 text-xs font-medium text-danger"><Icon className="size-3.5" aria-hidden="true" /><span>{tone.text}</span>{since && <span className="font-normal text-muted-foreground">· {relativeAge(since)}</span>}</p>;
}

function RunRow({ job }) {
  const title=jobDisplayTitle(job);
  const run=currentRun(job);
  const href=job.attention_reason === "GateAwaitingApproval" ? `#/gate/${encodeURIComponent(job.id)}` : `#/runs/${encodeURIComponent(job.id)}`;
  return <article className="border-b border-border last:border-b-0"><a href={href} className="flex items-center justify-between gap-4 px-5 py-4 hover:bg-muted/35" aria-label={`Open task ${title}`}><div className="min-w-0"><p className="truncate text-sm font-medium">{title}</p><p className="mt-1 text-xs text-muted-foreground">{job.repository} · {friendlyName(run?.command || job.command)}</p></div><div className="flex shrink-0 flex-col items-end gap-1">{job.attention_reason ? <AttentionTone reason={job.attention_reason} since={job.waiting_since} /> : <State value={job.state} />}<time className="text-xs text-muted-foreground" dateTime={job.created_at}>{relativeTime(job.created_at)}</time></div></a></article>;
}

function EmptyRuns({ filtered, openComposer }) {
  return <div className="grid place-items-center px-6 py-16 text-center"><span className="grid size-10 place-items-center rounded-full bg-muted text-muted-foreground"><GitBranch className="size-5" /></span><h3 className="mt-3 text-sm font-semibold">{filtered ? "No matching tasks" : "No tasks yet"}</h3><p className="mt-1 max-w-sm text-xs leading-5 text-muted-foreground">{filtered ? "Try a different state filter." : "Describe the work or paste an issue link to get started."}</p>{!filtered && <Button variant="outline" size="sm" className="mt-4" onClick={openComposer}><Plus className="size-3.5" />New task</Button>}</div>;
}

function MachinistMark() {
  return <svg className="machinist-mark" viewBox="0 0 64 48" aria-hidden="true">
    <path className="machinist-mark-piece-a" d="M8 40V18C8 11 12 7 18 7s10 4 10 11v10h4" />
    <path className="machinist-mark-piece-b" d="M32 28h4V18c0-7 4-11 10-11s10 4 10 11v22" />
  </svg>;
}

function selectionChoices(status) {
 const workflows=status.workflows || [];
 return workflows.length ? workflows.map(name=>({value:`workflow:${name}`,label:friendlyName(name)})) : (status.commands || []).map(name=>({value:`command:${name}`,label:friendlyName(name)}));
}
function firstSelection(status) { return selectionChoices(status)[0]?.value || ""; }
function shortId(id) { const [, value = id] = id.split("_", 2); return value.slice(0, 8); }
export const appRoot = createRoot(document.getElementById("root"));
appRoot.render(<App />);
