import { useCallback, useEffect, useRef, useState } from "react";

// createPoller is the framework-free core of usePolling. It fetches once on
// start and then every intervalMs after the previous fetch settles, but only
// while the document is visible. Hiding the tab stops the timer; showing it
// again fetches immediately and resumes the interval. Only the latest fetch
// reports a result, so a slow older response cannot overwrite a newer one.
export function createPoller({ fetcher, intervalMs, onResult = () => {}, document: doc = globalThis.document }) {
  let running = false;
  let timer;
  let sequence = 0;

  // Anything but "hidden" counts as visible (jsdom reports "prerender").
  const visible = () => doc?.visibilityState !== "hidden";

  function clear() {
    if (timer === undefined) return;
    clearTimeout(timer);
    timer = undefined;
  }

  function schedule() {
    clear();
    if (running && visible()) timer = setTimeout(poll, intervalMs);
  }

  async function poll() {
    clear();
    const current = ++sequence;
    let result;
    try {
      result = { kind: "success", data: await fetcher() };
    } catch (error) {
      result = { kind: "error", error: error instanceof Error ? error : new Error(String(error)) };
    }
    if (!running || current !== sequence) return;
    onResult(result);
    schedule();
  }

  function onVisibilityChange() {
    if (!running) return;
    if (visible()) poll();
    else clear();
  }

  return {
    start() {
      if (running) return;
      running = true;
      doc?.addEventListener("visibilitychange", onVisibilityChange);
      if (visible()) poll();
    },
    stop() {
      running = false;
      sequence += 1;
      clear();
      doc?.removeEventListener("visibilitychange", onVisibilityChange);
    },
    refresh() {
      return running ? poll() : Promise.resolve();
    },
  };
}

const idle = { data: undefined, error: undefined };

// usePolling polls fetcher every intervalMs while the tab is visible. Pass a
// stable fetcher (useCallback/useMemo); changing it restarts polling with fresh
// state, and passing null disables polling. A failed fetch keeps the last data.
export function usePolling(fetcher, intervalMs) {
  const [state, setState] = useState(idle);
  const poller = useRef(null);

  useEffect(() => {
    setState(idle);
    if (!fetcher) return undefined;
    const current = createPoller({
      fetcher,
      intervalMs,
      onResult: (result) => setState((previous) => result.kind === "success" ? { data: result.data, error: undefined } : { data: previous.data, error: result.error }),
    });
    poller.current = current;
    current.start();
    return () => {
      current.stop();
      if (poller.current === current) poller.current = null;
    };
  }, [fetcher, intervalMs]);

  const refresh = useCallback(() => poller.current ? poller.current.refresh() : Promise.resolve(), []);
  return { data: state.data, error: state.error, refresh };
}
