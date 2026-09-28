import React, { useEffect, useRef, useState } from "react";
import { ArrowLeft } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";
import { formatDurationMillis, formatTokenUsage } from "./run-metrics.js";
import { createLogFollower, initialLogState } from "./run-log.js";
import { State, friendlyName, formatTimestamp } from "./task-display.jsx";
import { createPoller } from "./use-polling.js";

const terminalStates = new Set(["succeeded", "failed", "timed_out", "cancelled"]);

async function fetchRun(path, csrfToken) {
  const response = await fetch(path, {
    headers: { Accept: "application/json", "X-Machinist-CSRF": csrfToken },
  });
  if (!response.ok) {
    const body = await response.json().catch(() => ({}));
    const error = new Error(body.error || `Run request failed (${response.status})`);
    error.status = response.status;
    throw error;
  }
  return response.json();
}

// useRunDetail polls GET /runs/{id} until the run reaches a terminal state.
function useRunDetail(runID, csrfToken) {
  const [state, setState] = useState({ run: undefined, error: undefined });
  useEffect(() => {
    setState({ run: undefined, error: undefined });
    if (!runID || !csrfToken) return undefined;
    const poller = createPoller({
      fetcher: () => fetchRun(`/api/v1/runs/${encodeURIComponent(runID)}`, csrfToken),
      intervalMs: 2000,
      onResult: (result) => {
        if (result.kind === "success") {
          setState({ run: result.data, error: undefined });
          if (terminalStates.has(result.data.state)) poller.stop();
        } else setState((previous) => ({ run: previous.run, error: result.error }));
      },
    });
    poller.start();
    return () => poller.stop();
  }, [runID, csrfToken]);
  return state;
}

function useRunLog(runID, csrfToken) {
  const [log, setLog] = useState(initialLogState);
  useEffect(() => {
    setLog(initialLogState());
    if (!runID || !csrfToken) return undefined;
    const follower = createLogFollower(
      (offset) => fetchRun(`/api/v1/runs/${encodeURIComponent(runID)}/log?offset=${offset}`, csrfToken),
      { onChange: setLog },
    );
    follower.start();
    return () => follower.stop();
  }, [runID, csrfToken]);
  return log;
}

export function RunDetail({ runID, csrfToken }) {
  const { run, error } = useRunDetail(runID, csrfToken);
  const log = useRunLog(runID, csrfToken);
  const back = run?.job_id ? `#/runs/${encodeURIComponent(run.job_id)}` : "#/runs";
  if (!run)
    return (
      <div className="p-8">
        <a href="#/runs" className="text-sm underline">
          Back to tasks
        </a>
        <p className="mt-4">
          {error?.status === 404 ? "Run not found." : "Loading run…"}
        </p>
        {error && error.status !== 404 && <p role="alert">{error.message}</p>}
      </div>
    );
  return (
    <div className="mx-auto max-w-[1000px] space-y-7 p-4 sm:p-6 lg:p-8">
      <header className="space-y-4">
        <Button asChild variant="ghost" size="sm" className="-ml-3">
          <a href={back}>
            <ArrowLeft className="size-4" />
            Back to task
          </a>
        </Button>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <h1 className="min-w-0 break-words text-2xl font-semibold">
            {friendlyName(run.command) || "Run"}
          </h1>
          <State value={run.state} />
        </div>
        <p className="font-mono text-sm text-muted-foreground">{run.id || runID}</p>
        {error && (
          <p role="alert" className="text-sm text-danger">
            {error.message}
          </p>
        )}
      </header>
      <Card className="space-y-4 p-5 sm:p-6" aria-label="Run result">
        <h2 className="text-lg font-semibold">Result</h2>
        {run.summary && (
          <p className="whitespace-pre-wrap text-sm leading-6">{run.summary}</p>
        )}
        {run.error && run.error !== run.summary && (
          <p role="alert" className="whitespace-pre-wrap break-words text-sm text-danger">
            {run.error}
          </p>
        )}
        {run.failure_class && (
          <p className="text-sm">
            Failure: <span className="font-medium">{friendlyName(run.failure_class)}</span>
            {run.reset_at && ` · resets ${formatTimestamp(run.reset_at)}`}
          </p>
        )}
        <dl className="grid gap-3 sm:grid-cols-3">
          <Metric label="Started" value={formatTimestamp(run.started_at)} />
          <Metric label="Completed" value={formatTimestamp(run.completed_at)} />
          <Metric label="Exit code" value={run.exit_code === undefined ? "Unavailable" : String(run.exit_code)} />
          <Metric label="Executor" value={run.executor || "Unknown"} />
          <Metric label="Worker" value={run.worker_name || "Unassigned"} />
          <Metric label="Model" value={run.model || "Executor default"} />
          <Metric
            label="Duration"
            value={Number.isSafeInteger(run.duration_millis) ? formatDurationMillis(run.duration_millis) : "Not available"}
          />
          <Metric
            label="Tokens"
            value={formatTokenUsage(run.token_usage) === "Unavailable" ? "Not reported" : formatTokenUsage(run.token_usage)}
          />
        </dl>
        {run.result != null && (
          <Block label="Structured result">{JSON.stringify(run.result, null, 2)}</Block>
        )}
      </Card>
      <LogPane log={log} />
      {run.events && (
        <Card className="space-y-3 p-5 sm:p-6" aria-label="Run events">
          <h2 className="text-lg font-semibold">Events</h2>
          <Block label="Events">{run.events}</Block>
        </Card>
      )}
      <Card className="space-y-3 p-5 sm:p-6" aria-label="Rendered prompt">
        <h2 className="text-lg font-semibold">Prompt</h2>
        <Block label="Prompt" wrap>
          {run.rendered_prompt || "No prompt recorded."}
        </Block>
      </Card>
    </div>
  );
}

function LogPane({ log }) {
  const pane = useRef(null);
  const stick = useRef(true);
  // Follow the tail unless the reader has scrolled up.
  useEffect(() => {
    if (pane.current && stick.current) pane.current.scrollTop = pane.current.scrollHeight;
  }, [log]);
  const onScroll = () => {
    const el = pane.current;
    stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
  };
  const empty = !log.entries.length;
  return (
    <Card className="space-y-3 p-5 sm:p-6" aria-label="Run log">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-lg font-semibold">Log</h2>
        <span className="text-xs text-muted-foreground">
          {log.done ? "Complete" : "Live"}
        </span>
      </div>
      {log.error && (
        <p role="alert" className="text-sm text-danger">
          {log.error.message}
        </p>
      )}
      <pre
        ref={pane}
        onScroll={onScroll}
        aria-label="Log output"
        aria-live="polite"
        className="max-h-[32rem] overflow-auto whitespace-pre-wrap break-words rounded-md bg-muted p-3 font-mono text-xs leading-5"
      >
        {empty
          ? log.done ? "No output." : "Waiting for output…"
          : log.entries.map((entry, index) =>
              entry.truncated ? (
                <span key={index} role="note" className="block italic text-muted-foreground">
                  … earlier output truncated …
                </span>
              ) : (
                <React.Fragment key={index}>{entry.data}</React.Fragment>
              ),
            )}
      </pre>
    </Card>
  );
}

function Block({ label, wrap = false, children }) {
  return (
    <pre
      aria-label={label}
      className={cn(
        "max-h-96 overflow-auto rounded-md bg-muted p-3 font-mono text-xs leading-5",
        wrap ? "whitespace-pre-wrap break-words" : "whitespace-pre",
      )}
    >
      {children}
    </pre>
  );
}

function Metric({ label, value }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 truncate text-sm" title={value}>
        {value}
      </dd>
    </div>
  );
}
