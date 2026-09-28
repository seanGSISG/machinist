import assert from "node:assert/strict";
import test from "node:test";
import { createPoller } from "./use-polling.js";

function fakeDocument(visibilityState = "visible") {
  const doc = new EventTarget();
  doc.visibilityState = visibilityState;
  doc.show = () => { doc.visibilityState = "visible"; doc.dispatchEvent(new Event("visibilitychange")); };
  doc.hide = () => { doc.visibilityState = "hidden"; doc.dispatchEvent(new Event("visibilitychange")); };
  return doc;
}

async function settle() {
  for (let i = 0; i < 5; i += 1) await Promise.resolve();
}

function setup(t, { visibilityState = "visible", fetcher } = {}) {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const doc = fakeDocument(visibilityState);
  const results = [];
  let fetches = 0;
  const poller = createPoller({
    fetcher: fetcher || (async () => ++fetches),
    intervalMs: 1000,
    onResult: (result) => results.push(result),
    document: doc,
  });
  t.after(() => poller.stop());
  return { doc, poller, results, fetches: () => fetches };
}

test("polls immediately and then every interval while visible", async (t) => {
  const { poller, results, fetches } = setup(t);
  poller.start();
  await settle();
  assert.equal(fetches(), 1);
  for (let i = 0; i < 3; i += 1) {
    t.mock.timers.tick(1000);
    await settle();
  }
  assert.equal(fetches(), 4);
  assert.deepEqual(results.at(-1), { kind: "success", data: 4 });
});

test("pauses while hidden, fetches once on becoming visible, then resumes", async (t) => {
  const { doc, poller, fetches } = setup(t);
  poller.start();
  await settle();
  assert.equal(fetches(), 1);

  doc.hide();
  for (let i = 0; i < 5; i += 1) {
    t.mock.timers.tick(1000);
    await settle();
  }
  assert.equal(fetches(), 1, "no fetches while hidden");

  doc.show();
  await settle();
  assert.equal(fetches(), 2, "exactly one fetch on becoming visible");
  t.mock.timers.tick(999);
  await settle();
  assert.equal(fetches(), 2);

  t.mock.timers.tick(1);
  await settle();
  assert.equal(fetches(), 3, "interval resumes");
  t.mock.timers.tick(1000);
  await settle();
  assert.equal(fetches(), 4);
});

test("does not fetch when started hidden until the tab becomes visible", async (t) => {
  const { doc, poller, fetches } = setup(t, { visibilityState: "hidden" });
  poller.start();
  for (let i = 0; i < 4; i += 1) {
    t.mock.timers.tick(1000);
    await settle();
  }
  assert.equal(fetches(), 0);
  doc.show();
  await settle();
  assert.equal(fetches(), 1);
});

test("refresh forces a fetch and restarts the interval", async (t) => {
  const { poller, fetches } = setup(t);
  poller.start();
  await settle();
  t.mock.timers.tick(600);
  await poller.refresh();
  await settle();
  assert.equal(fetches(), 2);
  t.mock.timers.tick(600);
  await settle();
  assert.equal(fetches(), 2, "the pending tick was replaced by the refresh");
  t.mock.timers.tick(400);
  await settle();
  assert.equal(fetches(), 3);
});

test("errors are reported and polling continues", async (t) => {
  let calls = 0;
  const { poller, results } = setup(t, { fetcher: async () => { calls += 1; if (calls === 1) throw new Error("offline"); return "ok"; } });
  poller.start();
  await settle();
  assert.equal(results[0].kind, "error");
  assert.equal(results[0].error.message, "offline");
  t.mock.timers.tick(1000);
  await settle();
  assert.deepEqual(results[1], { kind: "success", data: "ok" });
});

test("stop removes the listener, cancels the timer and drops in-flight results", async (t) => {
  let resolve;
  const { doc, poller, results } = setup(t, { fetcher: () => new Promise((done) => { resolve = done; }) });
  poller.start();
  poller.stop();
  resolve("late");
  await settle();
  assert.deepEqual(results, []);
  doc.hide();
  doc.show();
  t.mock.timers.tick(5000);
  await settle();
  assert.deepEqual(results, []);
});
