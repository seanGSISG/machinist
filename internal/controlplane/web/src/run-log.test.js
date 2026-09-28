import assert from "node:assert/strict";
import test from "node:test";
import { applyLogChunk, createLogFollower, initialLogState, logText } from "./run-log.js";

async function settle() {
  for (let i = 0; i < 5; i += 1) await Promise.resolve();
}

// fakeLog serves a scripted sequence of responses and records requested offsets.
function fakeLog(responses) {
  const offsets = [];
  const fetchLog = async (offset) => {
    offsets.push(offset);
    const next = responses[Math.min(offsets.length - 1, responses.length - 1)];
    if (next instanceof Error) throw next;
    return next;
  };
  return { fetchLog, offsets };
}

function follow(t, responses) {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const { fetchLog, offsets } = fakeLog(responses);
  const states = [];
  const follower = createLogFollower(fetchLog, { intervalMs: 1000, onChange: (state) => states.push(state), document: new EventTarget() });
  t.after(() => follower.stop());
  return { follower, offsets, states };
}

async function tick(t, times = 1) {
  for (let i = 0; i < times; i += 1) {
    t.mock.timers.tick(1000);
    await settle();
  }
}

test("advances to next_offset and appends each new chunk", async (t) => {
  const { follower, offsets } = follow(t, [
    { offset: 0, next_offset: 6, data: "hello\n", truncated: false, done: false },
    { offset: 6, next_offset: 12, data: "world\n", truncated: false, done: false },
    { offset: 12, next_offset: 12, data: "", truncated: false, done: false },
  ]);
  follower.start();
  await settle();
  await tick(t, 2);
  assert.deepEqual(offsets, [0, 6, 12]);
  assert.equal(follower.state().offset, 12);
  assert.equal(logText(follower.state()), "hello\nworld\n");
});

test("does not append duplicate data from repeated or overlapping responses", async (t) => {
  const { follower } = follow(t, [
    { offset: 0, next_offset: 6, data: "hello\n", truncated: false, done: false },
    { offset: 0, next_offset: 6, data: "hello\n", truncated: false, done: false },
    { offset: 3, next_offset: 12, data: "lo\nworld\n", truncated: false, done: false },
    { offset: 12, next_offset: 12, data: "", truncated: true, done: false },
  ]);
  follower.start();
  await settle();
  await tick(t, 3);
  assert.equal(logText(follower.state()), "hello\nworld\n");
  assert.equal(follower.state().entries.some((entry) => entry.truncated), false, "an empty caught-up poll is not a gap");
});

test("stops polling once the server reports done", async (t) => {
  const { follower, offsets, states } = follow(t, [
    { offset: 0, next_offset: 4, data: "one\n", truncated: false, done: false },
    { offset: 4, next_offset: 8, data: "two\n", truncated: false, done: true },
  ]);
  follower.start();
  await settle();
  await tick(t);
  assert.equal(follower.state().done, true);
  await tick(t, 5);
  assert.deepEqual(offsets, [0, 4], "no fetches after done");
  assert.equal(states.length, 2);
  assert.equal(logText(follower.state()), "one\ntwo\n");
});

test("shows a truncation marker when earlier output was dropped", async (t) => {
  const { follower } = follow(t, [
    { offset: 100, next_offset: 104, data: "tail", truncated: true, done: false },
    { offset: 200, next_offset: 205, data: "later", truncated: true, done: true },
  ]);
  follower.start();
  await settle();
  await tick(t);
  assert.deepEqual(follower.state().entries, [{ truncated: true }, { data: "tail" }, { truncated: true }, { data: "later" }]);
  assert.equal(logText(follower.state(), "…"), "…tail…later");
});

test("keeps the log and retries after a failed fetch", async (t) => {
  const { follower, offsets } = follow(t, [
    { offset: 0, next_offset: 2, data: "a\n", truncated: false, done: false },
    new Error("offline"),
    { offset: 2, next_offset: 4, data: "b\n", truncated: false, done: true },
  ]);
  follower.start();
  await settle();
  await tick(t);
  assert.equal(follower.state().error.message, "offline");
  assert.equal(logText(follower.state()), "a\n");
  await tick(t);
  assert.deepEqual(offsets, [0, 2, 2]);
  assert.equal(follower.state().error, undefined);
  assert.equal(logText(follower.state()), "a\nb\n");
});

test("skips overlapping bytes of multi-byte output by byte offset", () => {
  let state = applyLogChunk(initialLogState(), { offset: 0, next_offset: 3, data: "é!", done: false });
  state = applyLogChunk(state, { offset: 0, next_offset: 6, data: "é!ü", done: false });
  assert.equal(logText(state), "é!ü");
  assert.equal(state.offset, 6);
});

test("bounds the client-side log behind a truncation marker", () => {
  let state = applyLogChunk(initialLogState(), { offset: 0, next_offset: 6, data: "abcdef", done: false }, 4);
  assert.deepEqual(state.entries, [{ truncated: true }, { data: "cdef" }]);
  state = applyLogChunk(state, { offset: 6, next_offset: 8, data: "gh", done: false }, 4);
  assert.deepEqual(state.entries, [{ truncated: true }, { data: "efgh" }]);
});
