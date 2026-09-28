import { createPoller } from "./use-polling.js";

// The browser keeps at most this many characters of a followed log; older
// output is dropped behind a truncation marker, like the server's bounded tail.
export const maxLogChars = 512 << 10;

const encoder = new TextEncoder();
const decoder = new TextDecoder();

export function initialLogState() {
  return { offset: 0, entries: [], done: false, error: undefined };
}

// logText joins the text entries, marking each truncation point with marker.
export function logText(state, marker = "[… earlier output truncated …]\n") {
  return state.entries.map((entry) => (entry.truncated ? marker : entry.data)).join("");
}

function pushText(entries, data) {
  if (!data) return;
  const last = entries.at(-1);
  if (last && !last.truncated) entries[entries.length - 1] = { data: last.data + data };
  else entries.push({ data });
}

function pushMarker(entries) {
  if (!entries.at(-1)?.truncated) entries.push({ truncated: true });
}

function bound(entries, limit) {
  const size = entries.reduce((total, entry) => total + (entry.data?.length || 0), 0);
  if (size <= limit) return entries;
  const kept = [];
  let room = limit;
  for (let i = entries.length - 1; i >= 0 && room > 0; i -= 1) {
    const entry = entries[i];
    if (entry.truncated) {
      kept.unshift(entry);
      continue;
    }
    kept.unshift(entry.data.length <= room ? entry : { data: entry.data.slice(-room) });
    room -= entry.data.length;
  }
  if (kept[0]?.truncated) kept.shift();
  return [{ truncated: true }, ...kept];
}

// applyLogChunk folds one GET /runs/{id}/log response into state. Offsets are
// byte positions: bytes before state.offset were already appended, so a
// repeated or overlapping response adds nothing twice, and a response starting
// past state.offset means output was lost and gets a truncation marker.
export function applyLogChunk(state, chunk, limit = maxLogChars) {
  const start = Number(chunk?.offset) || 0;
  const next = Number(chunk?.next_offset) || 0;
  const done = Boolean(chunk?.done);
  const entries = [...state.entries];
  if (start > state.offset || (chunk?.truncated && state.offset === 0 && !entries.length)) pushMarker(entries);
  if (next > state.offset && chunk?.data) {
    const skip = Math.max(0, state.offset - start);
    pushText(entries, skip ? decoder.decode(encoder.encode(chunk.data).slice(skip)) : chunk.data);
  }
  return { offset: Math.max(state.offset, next), entries: bound(entries, limit), done, error: undefined };
}

// createLogFollower polls fetchLog(offset) through the usePolling core,
// appending new output and advancing to next_offset, until a response reports
// done=true. onChange receives every new state; failed fetches keep the log
// and retry on the next interval.
export function createLogFollower(fetchLog, { intervalMs = 1000, onChange = () => {}, document, limit = maxLogChars } = {}) {
  let state = initialLogState();
  const poller = createPoller({
    fetcher: () => fetchLog(state.offset),
    intervalMs,
    document,
    onResult: (result) => {
      state = result.kind === "success" ? applyLogChunk(state, result.data, limit) : { ...state, error: result.error };
      if (state.done) poller.stop();
      onChange(state);
    },
  });
  return {
    start: () => { if (!state.done) poller.start(); },
    stop: () => poller.stop(),
    state: () => state,
  };
}
