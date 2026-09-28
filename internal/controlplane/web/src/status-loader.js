// fetchStatus reads the slim /status snapshot. It returns the body untouched so
// new fields reach the UI without changes here.
export async function fetchStatus(request = globalThis.fetch) {
  const response = await request("/api/v1/status", { headers: { Accept: "application/json" } });
  if (!response.ok) throw new Error(`Status request failed (${response.status})`);
  return response.json();
}

export function createStatusLoader({ request = fetchStatus, apply }) {
  let latestRequest = 0;

  async function refresh() {
    const requestNumber = ++latestRequest;
    try {
      const status = await request();
      if (requestNumber !== latestRequest) return;
      apply({ kind: "success", status });
    } catch (error) {
      if (requestNumber !== latestRequest) return;
      apply({ kind: "error", message: error instanceof Error ? error.message : String(error) });
    }
  }

  return {
    refresh,
    cancel() { latestRequest += 1; },
  };
}
