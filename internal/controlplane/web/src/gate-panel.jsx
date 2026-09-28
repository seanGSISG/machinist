import React, { useCallback, useEffect, useState } from "react";
import Markdown from "react-markdown";
import { ArrowLeft } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { PageHeading } from "@/components/ui/page-heading";
import { jobDisplayTitle } from "./runs-board.js";
import { State, friendlyName } from "./task-display.jsx";

// Raw HTML in an artifact is shown as literal text. Without rehype-raw
// react-markdown would silently drop it; converting html nodes to text keeps
// what the author wrote visible without ever creating markup from it.
function htmlAsText() {
  const visit = (node) => {
    if (node.type === "html") Object.assign(node, { type: "text" });
    node.children?.forEach(visit);
  };
  return visit;
}

async function gateRequest(path, csrfToken, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: { Accept: "application/json", "X-Machinist-CSRF": csrfToken, ...options.headers },
  });
  if (!response.ok) {
    const body = await response.json().catch(() => ({}));
    const error = new Error(body.error || `Request failed (${response.status})`);
    error.status = response.status;
    throw error;
  }
  return response;
}

// The route only carries the job ID, so the panel fetches the CSRF token from
// /status itself when the shell has not passed one yet.
async function resolveCSRF(csrfToken) {
  if (csrfToken) return csrfToken;
  const response = await fetch("/api/v1/status", { headers: { Accept: "application/json" } });
  if (!response.ok) throw new Error(`Status request failed (${response.status})`);
  return (await response.json()).csrf_token || "";
}

const isMarkdown = (file) => /\.(md|markdown)$/i.test(file.path) || file.content_type === "text/markdown";

// gatedRunID is the run whose output the reviewer is deciding on: the one the
// gate records, else the last completed run before the gate.
function gatedRunID(job) {
  const latest = job.runs.at(-1);
  if (latest?.reviewed_run_id) return latest.reviewed_run_id;
  return job.runs.slice(0, -1).findLast((run) => run.outcome === "complete")?.id || "";
}

async function loadGate(jobID, csrfToken) {
  const token = await resolveCSRF(csrfToken);
  const job = await (await gateRequest(`/api/v1/jobs/${encodeURIComponent(jobID)}`, token)).json();
  const runID = gatedRunID(job);
  let documents = [];
  if (runID && job.task) {
    const files = await (await gateRequest(`/api/v1/jobs/${encodeURIComponent(jobID)}/artifacts`, token)).json();
    const gated = files.filter((file) => file.run_id === runID && isMarkdown(file));
    documents = await Promise.all(gated.map(async (file) => ({
      path: file.path,
      text: await (await gateRequest(`/api/v1/artifacts/${encodeURIComponent(file.id)}/content`, token)).text(),
    })));
  }
  return { token, job, runID, documents };
}

export function GatePanel({ jobID, csrfToken }) {
  const [gate, setGate] = useState(null);
  const [error, setError] = useState("");
  const [requesting, setRequesting] = useState(false);
  const [feedback, setFeedback] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const loaded = await loadGate(jobID, csrfToken);
      setGate(loaded);
      setError("");
    } catch (loadError) {
      setError(loadError.status === 404 ? "Task not found." : loadError.message);
    }
  }, [jobID, csrfToken]);
  useEffect(() => {
    setGate(null);
    load();
  }, [load]);

  const job = gate?.job.id === jobID ? gate.job : undefined;
  const latest = job?.runs.at(-1);
  const awaiting = job?.state === "awaiting_approval";

  async function decide(action) {
    setBusy(true);
    setError("");
    try {
      await gateRequest(`/api/v1/jobs/${encodeURIComponent(job.id)}/${action}`, gate.token, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ run_id: latest?.id, previous_process_stopped: false, feedback: action === "request_changes" ? feedback : "" }),
      });
      setRequesting(false);
      setFeedback("");
      await load();
    } catch (actionError) {
      setError(actionError.message);
    } finally {
      setBusy(false);
    }
  }

  const reviewed = job?.runs.find((run) => run.id === gate.runID);
  return (
    <div className="flex min-h-dvh flex-col">
      <div className="mx-auto w-full max-w-[1000px] flex-1 space-y-6 p-4 sm:p-6 lg:p-8">
        <Button asChild variant="ghost" size="sm" className="-ml-3">
          <a href={`#/runs/${encodeURIComponent(jobID)}`}>
            <ArrowLeft className="size-4" />
            Open task
          </a>
        </Button>
        <PageHeading title="Approval gate" description={job ? jobDisplayTitle(job) : `Task ${jobID}`} />
        {job && (
          <div className="flex flex-wrap items-center gap-3 text-sm text-muted-foreground">
            <State value={job.state} />
            <span>{job.repository}</span>
            {reviewed && <span>Reviewing {friendlyName(reviewed.command).toLowerCase()} output</span>}
          </div>
        )}
        {error && <p role="alert" className="text-sm text-danger">{error}</p>}
        {!job && !error && <p>Loading gate…</p>}
        {job && !awaiting && <p className="text-sm text-muted-foreground">This task is not waiting for approval.</p>}
        {job && gate.documents.map((document) => (
          <Card key={document.path} className="space-y-3 p-4 sm:p-6">
            <h2 className="text-sm font-medium text-muted-foreground">{document.path}</h2>
            <div className="gate-markdown min-w-0 space-y-3 break-words text-sm leading-6">
              <Markdown remarkPlugins={[htmlAsText]}>{document.text}</Markdown>
            </div>
          </Card>
        ))}
        {job && !gate.documents.length && (
          <Card className="space-y-3 p-4 sm:p-6">
            <div className="gate-markdown min-w-0 space-y-3 break-words text-sm leading-6">
              <Markdown remarkPlugins={[htmlAsText]}>{reviewed?.summary || "No output was saved for review."}</Markdown>
            </div>
          </Card>
        )}
      </div>
      {job && awaiting && (
        <div
          className="sticky bottom-0 z-10 border-t border-border bg-background px-4 pt-3"
          style={{ paddingBottom: "calc(0.75rem + env(safe-area-inset-bottom))" }}
        >
          <div className="mx-auto max-w-[1000px] space-y-3">
            {requesting && (
              <label className="block">
                <span className="field-label">What needs to change?</span>
                <textarea
                  className="field-control min-h-24"
                  value={feedback}
                  onChange={(event) => setFeedback(event.target.value)}
                  maxLength={4000}
                  placeholder="Explain what to revise in the previous stage’s result."
                />
              </label>
            )}
            <div className="flex gap-2 [&>*]:flex-1">
              {requesting ? (
                <>
                  <Button variant="ghost" disabled={busy} onClick={() => setRequesting(false)}>Keep reviewing</Button>
                  <Button disabled={busy || !feedback.trim()} onClick={() => decide("request_changes")}>
                    {busy ? "Submitting…" : "Send feedback"}
                  </Button>
                </>
              ) : (
                <>
                  {latest?.reviewed_run_id && (
                    <Button variant="outline" disabled={busy} onClick={() => setRequesting(true)}>Request changes</Button>
                  )}
                  <Button disabled={busy} onClick={() => decide("approve")}>{busy ? "Submitting…" : "Approve"}</Button>
                </>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
