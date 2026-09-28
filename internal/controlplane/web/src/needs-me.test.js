import assert from "node:assert/strict";
import test from "node:test";
import { attentionTone, needsMe, relativeAge, titleFor } from "./needs-me.js";

test("needsMe combines every server-derived attention collection", () => {
  const gate = { id: "gate" };
  const blocked = { id: "blocked" };
  const login = { id: "worker/codex" };
  assert.deepEqual(needsMe({ gates_awaiting_approval: [gate], blocked_jobs: [blocked], logins: [login] }), [gate, blocked, login]);
  assert.deepEqual(needsMe({}), []);
});

test("titleFor includes the attention count only when nonzero", () => {
  assert.equal(titleFor(3), "(3) Machinist");
  assert.equal(titleFor(0), "Machinist");
});

test("relativeAge uses an appropriate relative-time unit", () => {
  const now = new Date("2026-09-27T12:00:00Z");
  assert.equal(relativeAge("2026-09-27T11:55:00Z", now), "5 minutes ago");
  assert.equal(relativeAge("2026-09-27T14:00:00Z", now), "in 2 hours");
  assert.equal(relativeAge("not-a-time", now), "");
});

test("attention tones pair an icon identifier with readable text", () => {
  for (const reason of ["GateAwaitingApproval", "Blocked", "LoginExpired", "LoginRechecking", "FutureReason"]) {
    const tone = attentionTone(reason);
    assert.ok(tone.icon);
    assert.ok(tone.text);
  }
});
