export const LOGIN_TOKEN_HEADER = "X-Machinist-Login-Token";

export const loginKeys = [
  { key: "enter", label: "Enter" },
  { key: "up", label: "↑" },
  { key: "down", label: "↓" },
  { key: "tab", label: "Tab" },
  { key: "escape", label: "Esc" },
];

const stateLabels = { connected: "Connected", expiring: "Expiring", expired: "Expired", unknown: "Unknown" };

export function connectionStateLabel(state) {
  return stateLabels[state] || "Unknown";
}

export function connectionTone(state) {
  if (state === "connected") return "success";
  if (state === "expiring") return "warning";
  if (state === "expired") return "danger";
  return "muted";
}

export function loginActive(state) {
  return ["pending", "running", "awaiting_input"].includes(state);
}

const loginResults = {
  succeeded: "Logged in. The worker is checking the new credentials.",
  failed: "The login did not finish.",
  cancelled: "Login cancelled.",
  timed_out: "The login timed out.",
};

export function loginResult(session) {
  const text = loginResults[session.state] || "";
  return session.error ? `${text} ${session.error}`.trim() : text;
}

export function loginStep(session) {
  if (session.state === "pending") return "Waiting for the worker to start the login…";
  if (session.awaiting_input) return "Paste the code from the login page.";
  if (session.url && session.code) return "Open the login page and enter the code.";
  if (session.url) return "Open the login page and sign in.";
  if (session.code) return "Enter the code on the provider's device page.";
  return "The login is running. Follow the terminal output below.";
}

// Worker-reported links are shown only when they are plain https links.
export function safeLoginURL(value) {
  if (typeof value !== "string" || !value) return "";
  try {
    const parsed = new URL(value);
    return parsed.protocol === "https:" && !parsed.username && !parsed.password ? parsed.href : "";
  } catch {
    return "";
  }
}

// Expired executors appear in the Needs attention column: runs for them are not leased.
export function expiredConnections(connections = []) {
  return connections.filter((connection) => connection.state === "expired");
}

export function groupConnectionsByWorker(connections = []) {
  const groups = new Map();
  for (const connection of connections) {
    if (!groups.has(connection.worker)) groups.set(connection.worker, { worker: connection.worker, online: false, connections: [] });
    const group = groups.get(connection.worker);
    group.online ||= Boolean(connection.online);
    group.connections.push(connection);
  }
  return [...groups.values()];
}

// The login token lives only in page memory; it is never stored.
export async function connectionsRequest(path, { method = "GET", body, csrfToken, loginToken } = {}) {
  const response = await fetch(path, {
    method,
    headers: {
      Accept: "application/json",
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
      ...(method !== "GET" ? { "X-Machinist-CSRF": csrfToken || "" } : {}),
      ...(loginToken ? { [LOGIN_TOKEN_HEADER]: loginToken } : {}),
    },
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
  });
  const result = response.status === 204 ? {} : await response.json().catch(() => ({}));
  if (!response.ok) {
    const error = new Error(result.error || `Connections request failed (${response.status})`);
    error.status = response.status;
    throw error;
  }
  return result;
}
