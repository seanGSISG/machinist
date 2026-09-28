import assert from "node:assert/strict";
import test from "node:test";
import { act, createElement } from "react";
import { JSDOM } from "jsdom";
import { createServer } from "vite";

// Local 09:05 today, so the expected HH:MM holds in any test time zone.
const until = new Date(2026, 8, 27, 9, 5).toISOString();
const limited = { id: "colo/codex", worker: "colo", executor: "codex", state: "rate_limited", since: "2026-09-27T08:00:00Z", reason: "RateLimited", message: "codex on colo is rate limited.", rate_limited_until: until, reset_source: "estimate" };

async function loadBadge(context) {
  const server = await createServer({ server: { middlewareMode: true }, appType: "custom", logLevel: "silent" });
  context.after(() => server.close());
  return server.ssrLoadModule("/src/executor-badge.jsx");
}

test("labels show the local reset time and mark estimates", async (context) => {
  const { clearRateLimitPath, clockTime, executorBadgeLabel } = await loadBadge(context);
  assert.equal(clockTime(until), "09:05");
  assert.equal(clockTime("not a time"), "");
  assert.equal(executorBadgeLabel(limited), "rate limited until 09:05 (est.)");
  assert.equal(executorBadgeLabel({ ...limited, reset_source: "structured" }), "rate limited until 09:05");
  assert.equal(executorBadgeLabel({ ...limited, reset_source: null, rate_limited_until: null }), "rate limited");
  assert.equal(executorBadgeLabel({ ...limited, state: "ok", rate_limited_until: null, reset_source: null }), "Available");
  assert.equal(clearRateLimitPath({ worker: "colo box", executor: "codex/x" }), "/api/v1/executors/colo%20box/codex%2Fx/clear-rate-limit");
});

test("the Clear button posts to the executor's clear route with the CSRF token", async (context) => {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/#/connections" });
  const priorGlobals = new Map();
  for (const name of ["window", "document", "navigator", "fetch", "IS_REACT_ACT_ENVIRONMENT"]) priorGlobals.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
  for (const name of ["window", "document", "navigator"]) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: dom.window[name] });
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const calls = [];
  globalThis.fetch = async (path, options) => {
    calls.push({ path, options });
    return { ok: true, status: 200, json: async () => ({ worker: "colo", executor: "codex", state: "ok" }) };
  };
  const { ExecutorBadge } = await loadBadge(context);
  const { createRoot } = await import("react-dom/client");
  const root = createRoot(document.getElementById("root"));
  context.after(async () => {
    await act(async () => root.unmount());
    dom.window.close();
    for (const [name, descriptor] of priorGlobals) {
      if (descriptor === undefined) delete globalThis[name];
      else Object.defineProperty(globalThis, name, descriptor);
    }
  });

  let cleared = 0;
  await act(async () => root.render(createElement(ExecutorBadge, { executor: limited, csrfToken: "csrf-1", onCleared: () => { cleared += 1; } })));
  assert.match(document.body.textContent, /rate limited until 09:05 \(est\.\)/);
  const button = [...document.querySelectorAll("button")].find((element) => element.textContent === "Clear");
  assert.ok(button, "a rate-limited executor has a Clear button");

  await act(async () => button.click());
  assert.equal(calls.length, 1);
  assert.equal(calls[0].path, "/api/v1/executors/colo/codex/clear-rate-limit");
  assert.equal(calls[0].options.method, "POST");
  assert.equal(calls[0].options.headers["X-Machinist-CSRF"], "csrf-1");
  assert.equal(cleared, 1);

  await act(async () => root.render(createElement(ExecutorBadge, { executor: { ...limited, state: "ok", rate_limited_until: null, reset_source: null }, csrfToken: "csrf-1" })));
  assert.match(document.body.textContent, /Available/);
  assert.equal(document.querySelectorAll("button").length, 0, "available executors have nothing to clear");
});
