import assert from "node:assert/strict";
import test from "node:test";
import { formatUsageKey, formatUsageNumber, usageRequestURL, usageState } from "./usage-state.js";

test("usageState describes loading, errors, empty data, and rollup rows", () => {
  assert.deepEqual(usageState({}), { kind: "loading", rows: [] });
  assert.deepEqual(usageState({ data: { rows: [] } }), { kind: "empty", rows: [] });

  const rows = [{ key: "codex", runs: 2, input_tokens: 150 }];
  assert.deepEqual(usageState({ data: { rows } }), { kind: "ready", rows });
  assert.deepEqual(usageState({ data: { rows }, error: new Error("offline") }), { kind: "error", message: "offline", rows });
});

test("usageRequestURL includes the selected grouping and optional since filter", () => {
  assert.equal(usageRequestURL("executor"), "/api/v1/usage?group_by=executor");
  assert.equal(usageRequestURL("ticket", "2026-09-20T12:00:00Z"), "/api/v1/usage?group_by=ticket&since=2026-09-20T12%3A00%3A00Z");
});

test("usage formatters distinguish missing dimensions and invalid totals", () => {
  assert.equal(formatUsageKey(""), "(none)");
  assert.equal(formatUsageKey("FAC-06"), "FAC-06");
  assert.equal(formatUsageNumber(12345), new Intl.NumberFormat().format(12345));
  assert.equal(formatUsageNumber(undefined), "—");
});
