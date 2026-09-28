import assert from "node:assert/strict";
import test from "node:test";
import { groupJobs, noTicketLabel, visibleJobs } from "./ticket-board.js";

const job = (id, ticket, updated_at, extra = {}) => ({ id, state: "succeeded", labels: ticket ? { ticket } : {}, updated_at, ...extra });

test("grouping by ticket orders groups by most recent activity and collects unlabeled jobs", () => {
  const jobs = [
    job("a1", "T1", "2026-09-27T10:00:00Z"),
    job("b1", "T2", "2026-09-27T12:00:00Z"),
    job("n1", "", "2026-09-27T11:00:00Z"),
    job("a2", "T1", "2026-09-27T13:00:00Z"),
    { id: "n2", state: "queued", created_at: "2026-09-27T09:00:00Z" },
  ];
  const groups = groupJobs(jobs, "ticket");
  assert.deepEqual(groups.map(({ label }) => label), ["T1", "T2", noTicketLabel]);
  assert.deepEqual(groups[0].jobs.map(({ id }) => id), ["a1", "a2"]);
  assert.deepEqual(groups[2].jobs.map(({ id }) => id), ["n1", "n2"]);
});

test("grouping mode none keeps a single group in input order", () => {
  const jobs = [job("a", "T1", "2026-09-27T10:00:00Z"), job("b", "T2", "2026-09-27T12:00:00Z")];
  assert.deepEqual(groupJobs(jobs, "none"), [{ key: "all", label: "", jobs }]);
  assert.deepEqual(groupJobs([], "none"), []);
  assert.deepEqual(groupJobs([], "ticket"), []);
});

test("superseded cancelled jobs are hidden unless requested", () => {
  const jobs = [
    job("hidden", "T1", "", { superseded: true, state: "cancelled" }),
    job("running", "T1", "", { superseded: true, state: "running" }),
    job("cancelled", "T1", "", { state: "cancelled" }),
    job("done", "T1", ""),
  ];
  assert.deepEqual(visibleJobs(jobs).map(({ id }) => id), ["running", "cancelled", "done"]);
  assert.deepEqual(visibleJobs(jobs, { showSuperseded: false }).map(({ id }) => id), ["running", "cancelled", "done"]);
  assert.deepEqual(visibleJobs(jobs, { showSuperseded: true }).map(({ id }) => id), jobs.map(({ id }) => id));
});
