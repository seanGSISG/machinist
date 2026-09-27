import assert from "node:assert/strict";
import test from "node:test";
import { routeFromHash } from "./routes.js";

test("routeFromHash recognizes task detail routes", () => {
  assert.deepEqual(routeFromHash("#/runs/job_123"), { view: "task", jobID: "job_123" });
  assert.deepEqual(routeFromHash("#/runs/job%2F123"), { view: "task", jobID: "job/123" });
});

test("routeFromHash recognizes the settings page", () => {
  assert.deepEqual(routeFromHash("#/settings"), { view: "settings", jobID: "" });
  assert.deepEqual(routeFromHash("#/connections"), { view: "connections", jobID: "" });
});

test("routeFromHash recognizes FAC-06 detail and usage routes", () => {
  assert.deepEqual(routeFromHash("#/run/run_123"), { view: "run", runID: "run_123" });
  assert.deepEqual(routeFromHash("#/run/run%2F123"), { view: "run", runID: "run/123" });
  assert.deepEqual(routeFromHash("#/gate/job_123"), { view: "gate", jobID: "job_123" });
  assert.deepEqual(routeFromHash("#/gate/job%2F123"), { view: "gate", jobID: "job/123" });
  assert.deepEqual(routeFromHash("#/usage"), { view: "usage" });
});

test("routeFromHash falls back to runs for incomplete or malformed routes", () => {
  assert.deepEqual(routeFromHash("#/runs/"), { view: "runs", jobID: "" });
  assert.deepEqual(routeFromHash("#/runs/%E0%A4%A"), { view: "runs", jobID: "" });
  assert.deepEqual(routeFromHash("#/run/"), { view: "runs", jobID: "" });
  assert.deepEqual(routeFromHash("#/gate/%E0%A4%A"), { view: "runs", jobID: "" });
  assert.deepEqual(routeFromHash("#/unknown"), { view: "runs", jobID: "" });
});
