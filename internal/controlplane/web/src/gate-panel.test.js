import assert from "node:assert/strict";
import test from "node:test";
import React, { act } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";

const gatedJob = {
  id: "job_gate", state: "awaiting_approval", repository: "example/repo", command: "build",
  task: { title: "Gated plan", spec: "Plan the feature" },
  workflow: { name: "plan-build", steps: ["plan", "build"], current_step: 1 },
  created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:01:00Z",
  runs: [
    { id: "run_plan", command: "plan", state: "succeeded", outcome: "complete", summary: "Planned" },
    { id: "run_build", command: "build", state: "awaiting_approval", reviewed_run_id: "run_plan" },
  ],
};

async function mountGate(context, handler) {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/#/gate/job_gate" });
  const prior = new Map();
  for (const name of ["window", "document", "navigator", "Event", "MouseEvent", "HTMLElement", "fetch", "IS_REACT_ACT_ENVIRONMENT"]) {
    prior.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    const value = name === "fetch" ? handler : name === "IS_REACT_ACT_ENVIRONMENT" ? true : dom.window[name];
    Object.defineProperty(globalThis, name, { configurable: true, writable: true, value });
  }
  const server = await createServer({ server: { middlewareMode: true }, appType: "custom", logLevel: "silent" });
  const { GatePanel } = await server.ssrLoadModule("/src/gate-panel.jsx");
  // react-dom decides whether it can use the DOM on load, so import it after the globals exist.
  const { createRoot } = await import("react-dom/client");
  const root = createRoot(document.getElementById("root"));
  context.after(async () => {
    await act(async () => root.unmount());
    await server.close();
    dom.window.close();
    for (const [name, descriptor] of prior) {
      if (descriptor === undefined) delete globalThis[name];
      else Object.defineProperty(globalThis, name, descriptor);
    }
  });
  await act(async () => root.render(React.createElement(GatePanel, { jobID: "job_gate" })));
  return dom;
}

function fixtureFetch(calls, markdown) {
  return async (url, options = {}) => {
    calls.push({ url, options });
    if (url === "/api/v1/status") return { ok: true, json: async () => ({ csrf_token: "csrf_gate" }) };
    if (url === "/api/v1/jobs/job_gate") return { ok: true, json: async () => structuredClone(gatedJob) };
    if (url === "/api/v1/jobs/job_gate/artifacts") return { ok: true, json: async () => [
      { id: "file_plan", run_id: "run_plan", path: "plan.md", size: 10, content_type: "text/markdown" },
      { id: "file_other", run_id: "run_build", path: "build.md", size: 10, content_type: "text/markdown" },
    ] };
    if (url === "/api/v1/artifacts/file_plan/content") return { ok: true, text: async () => markdown };
    if (options.method === "POST") return { ok: true, json: async () => ({}) };
    return { ok: false, status: 404, json: async () => ({ error: "not found" }) };
  };
}

async function eventually(check) {
  let failure;
  for (let attempt = 0; attempt < 100; attempt += 1) {
    try {
      return check();
    } catch (error) {
      failure = error;
      await act(async () => new Promise((resolve) => setTimeout(resolve, 10)));
    }
  }
  throw failure;
}

function button(label) {
  const match = [...document.querySelectorAll("button")].find((element) => element.textContent === label);
  assert.ok(match, `button ${label} should exist`);
  return match;
}

test("gated markdown renders raw HTML as text and never as elements", async (context) => {
  const calls = [];
  await mountGate(context, fixtureFetch(calls, "# The plan\n\n<script>alert(1)</script>\n\nInline <img src=x onerror=alert(2)> **bold**\n"));
  await eventually(() => assert.ok(document.querySelector("h1, h2, h3") && document.body.textContent.includes("The plan")));
  assert.equal(document.querySelector(".gate-markdown h1").textContent, "The plan");
  assert.equal(document.querySelector(".gate-markdown strong").textContent, "bold");
  assert.match(document.body.textContent, /<script>alert\(1\)<\/script>/);
  assert.match(document.body.textContent, /<img src=x onerror=alert\(2\)>/);
  assert.equal(document.querySelectorAll("script").length, 0, "no script element exists");
  assert.equal(document.querySelectorAll("img").length, 0, "no inline HTML element exists");
  assert.doesNotMatch(document.body.textContent, /build\.md/, "only the gated run's artifact is shown");
  const artifactCall = calls.find((call) => call.url === "/api/v1/artifacts/file_plan/content");
  assert.equal(artifactCall.options.headers["X-Machinist-CSRF"], "csrf_gate");
});

test("the sticky action bar approves and requests changes on the job action routes", async (context) => {
  const calls = [];
  await mountGate(context, fixtureFetch(calls, "Plan body"));
  await eventually(() => button("Approve"));
  const bar = button("Approve").closest(".sticky");
  assert.ok(bar.classList.contains("bottom-0"));
  assert.match(bar.getAttribute("style"), /env\(safe-area-inset-bottom\)/);

  await act(async () => button("Approve").click());
  await eventually(() => assert.ok(calls.some((call) => call.options.method === "POST")));
  const approve = calls.find((call) => call.options.method === "POST");
  assert.equal(approve.url, "/api/v1/jobs/job_gate/approve");
  assert.equal(approve.options.headers["X-Machinist-CSRF"], "csrf_gate");
  assert.deepEqual(JSON.parse(approve.options.body), { run_id: "run_build", previous_process_stopped: false, feedback: "" });

  await eventually(() => button("Request changes"));
  await act(async () => button("Request changes").click());
  const textarea = document.querySelector("textarea");
  const setValue = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, "value").set;
  await act(async () => {
    setValue.call(textarea, "Split step two");
    textarea.dispatchEvent(new window.Event("input", { bubbles: true }));
  });
  await eventually(() => assert.equal(button("Send feedback").disabled, false));
  await act(async () => button("Send feedback").click());
  await eventually(() => assert.equal(calls.filter((call) => call.options.method === "POST").length, 2));
  const request = calls.filter((call) => call.options.method === "POST")[1];
  assert.equal(request.url, "/api/v1/jobs/job_gate/request_changes");
  assert.deepEqual(JSON.parse(request.options.body), { run_id: "run_build", previous_process_stopped: false, feedback: "Split step two" });
});
