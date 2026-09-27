import assert from "node:assert/strict";
import test from "node:test";
import { commandForm, commandOverride, describeVersion, executorSaveMode, moveStep, promptWarnings, rememberNotice, settingsRequest, sourceLabel, takeNotice, workflowOverride, workflowSteps } from "./settings.js";

test("prompt warnings trigger above 2 KB or 40 non-empty lines but never for small prompts", () => {
  assert.deepEqual(promptWarnings("Plan {{task.spec}}\n\n\n"), []);
  assert.equal(promptWarnings("x".repeat(2049)).length, 1);
  assert.match(promptWarnings("step\n\n".repeat(41))[0], /41 non-empty lines/);
  assert.equal(promptWarnings("a long instruction line\n".repeat(100)).length, 2);
});

test("command overrides keep only set fields and clear when empty", () => {
  assert.equal(commandOverride(commandForm({ override: null })), null);
  assert.deepEqual(commandOverride({ executor: " codex ", model: "", timeout: "2m", prompt: "  " }), { executor: "codex", timeout: "2m" });
  assert.deepEqual(commandOverride({ executor: "", model: "sol", timeout: "", prompt: "Do {{task.spec}}\n" }), { model: "sol", prompt: "Do {{task.spec}}\n" });
  assert.deepEqual(commandForm({ override: { model: "opus" } }), { executor: "", model: "opus", timeout: "", prompt: "" });
});

test("workflow steps round-trip and reorder without losing fields", () => {
  const steps = workflowSteps({ effective: { steps: [{ command: "plan" }, { command: "build", approval: true, required_outputs: ["plan.md"] }] } });
  assert.deepEqual(steps.map((step) => step.approval), [false, true]);
  const moved = moveStep(steps, 1, -1);
  assert.deepEqual(moved.map((step) => step.command), ["build", "plan"]);
  assert.equal(moveStep(steps, 0, -1), steps);
  assert.deepEqual(workflowOverride(moved), { steps: [{ command: "build", approval: true, required_outputs: ["plan.md"] }, { command: "plan" }] });
});

test("versions and sources describe where a setting comes from", () => {
  assert.equal(describeVersion({ value: null }), "Override removed; config.toml applies");
  assert.equal(describeVersion({ value: {}, reverted_from: 3 }), "Reverted to version 3");
  assert.equal(sourceLabel({ file: {}, override: null }), "config.toml");
  assert.equal(sourceLabel({ file: {}, override: {} }), "Overridden");
  assert.equal(sourceLabel({ file: null, override: {} }), "Created in UI");
});

test("settings mutations send the CSRF token and surface server errors", async () => {
  const calls = [];
  globalThis.fetch = async (path, options) => {
    calls.push({ path, options });
    return { ok: false, status: 409, json: async () => ({ error: "this setting changed" }) };
  };
  await assert.rejects(settingsRequest("/api/v1/settings/commands/plan", { method: "PUT", body: { base_version: 1, value: null }, csrfToken: "csrf_x" }), /this setting changed/);
  assert.equal(calls[0].options.headers["X-Machinist-CSRF"], "csrf_x");
  assert.equal(calls[0].options.body, JSON.stringify({ base_version: 1, value: null }));
});

test("save notices survive one editor remount, then clear", () => {
  rememberNotice("commands/plan", { tone: "success", text: "Saved version 4." });
  assert.equal(takeNotice("commands/plan").text, "Saved version 4.");
  assert.equal(takeNotice("commands/plan"), null);
  assert.equal(takeNotice("commands/other"), null);
});

test("stored executor defaults stay clearable when the executor can no longer take a model", () => {
  const online = { workers: ["colo"], supports_model: true, override: null };
  assert.equal(executorSaveMode(online), "save");
  assert.equal(executorSaveMode({ ...online, workers: [] }), "none");
  assert.equal(executorSaveMode({ ...online, workers: [], override: { default_model: "sol" } }), "clear");
  assert.equal(executorSaveMode({ ...online, supports_model: false, override: { default_model: "sol" } }), "clear");
});
