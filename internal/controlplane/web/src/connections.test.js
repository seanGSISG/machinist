import assert from "node:assert/strict";
import test from "node:test";
import { connectionStateLabel, connectionTone, connectionsRequest, expiredConnections, groupConnectionsByWorker, LOGIN_TOKEN_HEADER, loginActive, loginResult, loginStep, safeLoginURL } from "./connections.js";

test("connection states map to labels and tones", () => {
  assert.equal(connectionStateLabel("connected"), "Connected");
  assert.equal(connectionStateLabel("bogus"), "Unknown");
  assert.deepEqual(["connected", "expiring", "expired", "unknown"].map(connectionTone), ["success", "warning", "danger", "muted"]);
});

test("only plain https login links are rendered", () => {
  assert.equal(safeLoginURL("https://claude.ai/oauth/authorize?code=true"), "https://claude.ai/oauth/authorize?code=true");
  for (const value of ["javascript:alert(1)", "http://example.test", "data:text/html,x", "https://user:pw@example.test", "", null, "not a url"]) assert.equal(safeLoginURL(value), "");
});

test("expired connections need attention and connections group by worker", () => {
  const connections = [
    { worker: "colo", executor: "claude", state: "expired", online: true },
    { worker: "colo", executor: "codex", state: "connected", online: true },
    { worker: "spare", executor: "claude", state: "expiring", online: false },
  ];
  assert.deepEqual(expiredConnections(connections).map((connection) => connection.executor), ["claude"]);
  assert.deepEqual(expiredConnections(undefined), []);
  const groups = groupConnectionsByWorker(connections);
  assert.deepEqual(groups.map((group) => [group.worker, group.online, group.connections.length]), [["colo", true, 2], ["spare", false, 1]]);
});

test("login steps and results describe what to do next", () => {
  assert.equal(loginActive("awaiting_input"), true);
  assert.equal(loginActive("succeeded"), false);
  assert.match(loginStep({ state: "pending" }), /Waiting for the worker/);
  assert.match(loginStep({ state: "running", url: "https://x", code: "AB-12" }), /enter the code/);
  assert.match(loginStep({ state: "awaiting_input", awaiting_input: true }), /Paste the code/);
  assert.equal(loginResult({ state: "failed", error: "login exited: exit status 1" }), "The login did not finish. login exited: exit status 1");
});

test("login requests send the CSRF and login tokens and surface errors", async () => {
  const calls = [];
  globalThis.fetch = async (path, options) => {
    calls.push({ path, options });
    if (options.method === "POST") return { ok: true, status: 204, json: async () => { throw new Error("no body"); } };
    return { ok: false, status: 404, json: async () => ({ error: "login session not found" }) };
  };
  assert.deepEqual(await connectionsRequest("/api/v1/connections/sessions/login_1/input", { method: "POST", body: { text: "code" }, csrfToken: "csrf_x", loginToken: "logintoken_y" }), {});
  assert.equal(calls[0].options.headers["X-Machinist-CSRF"], "csrf_x");
  assert.equal(calls[0].options.headers[LOGIN_TOKEN_HEADER], "logintoken_y");
  await assert.rejects(connectionsRequest("/api/v1/connections/sessions/login_1", { loginToken: "logintoken_y" }), (error) => error.status === 404 && /not found/.test(error.message));
  assert.equal(calls[1].options.headers["X-Machinist-CSRF"], undefined);
});
