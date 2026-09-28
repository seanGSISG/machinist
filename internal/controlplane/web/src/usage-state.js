export const usageGroups = [
  { value: "executor", label: "Executor" },
  { value: "model", label: "Model" },
  { value: "command", label: "Command" },
  { value: "ticket", label: "Ticket" },
];

export function usageRequestURL(groupBy, since = "") {
  const parameters = new URLSearchParams({ group_by: groupBy });
  if (since) parameters.set("since", since);
  return `/api/v1/usage?${parameters}`;
}

export function usageState({ data, error }) {
  if (error) return { kind: "error", message: error instanceof Error ? error.message : String(error), rows: data?.rows || [] };
  if (!data) return { kind: "loading", rows: [] };
  const rows = Array.isArray(data.rows) ? data.rows : [];
  return { kind: rows.length ? "ready" : "empty", rows };
}

export function formatUsageKey(value) {
  return value === "" || value == null ? "(none)" : String(value);
}

export function formatUsageNumber(value) {
  const number = Number(value);
  return Number.isFinite(number) ? new Intl.NumberFormat().format(number) : "—";
}

export async function fetchUsage(groupBy, since = "") {
  const response = await fetch(usageRequestURL(groupBy, since), { headers: { Accept: "application/json" } });
  const result = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(result.error || `Usage request failed (${response.status})`);
  return result;
}
