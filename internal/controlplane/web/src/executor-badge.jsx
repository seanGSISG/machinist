import { useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { connectionsRequest } from "@/connections";

// Local wall-clock time, zero-padded: the operator reads it against their own clock.
export function clockTime(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return `${String(date.getHours()).padStart(2, "0")}:${String(date.getMinutes()).padStart(2, "0")}`;
}

export function executorRateLimited(executor) {
  return executor?.state === "rate_limited";
}

export function executorBadgeLabel(executor) {
  if (!executorRateLimited(executor)) return "Available";
  const time = executor.rate_limited_until ? clockTime(executor.rate_limited_until) : "";
  const label = time ? `rate limited until ${time}` : "rate limited";
  return executor.reset_source === "estimate" ? `${label} (est.)` : label;
}

export function clearRateLimitPath(executor) {
  return `/api/v1/executors/${encodeURIComponent(executor.worker)}/${encodeURIComponent(executor.executor)}/clear-rate-limit`;
}

export function clearRateLimit(executor, csrfToken) {
  return connectionsRequest(clearRateLimitPath(executor), { method: "POST", csrfToken });
}

export function ExecutorBadge({ executor, csrfToken, onCleared }) {
  const [clearing, setClearing] = useState(false);
  const [error, setError] = useState("");
  const limited = executorRateLimited(executor);

  async function clear() {
    setClearing(true);
    setError("");
    try {
      await clearRateLimit(executor, csrfToken);
      await onCleared?.();
    } catch (clearError) {
      setError(clearError.message);
    } finally {
      setClearing(false);
    }
  }

  return <span className="inline-flex flex-wrap items-center gap-2">
    <Badge className={`normal-case ${limited ? "border-warning/25 bg-warning/10 text-warning" : "border-success/25 bg-success/10 text-success"}`} title={executor.message || undefined}>{executorBadgeLabel(executor)}</Badge>
    {limited && <Button type="button" variant="outline" size="sm" className="text-xs!" disabled={clearing} onClick={clear} aria-label={`Clear rate limit for ${executor.executor} on ${executor.worker}`}>{clearing ? "Clearing…" : "Clear"}</Button>}
    {error && <span role="alert" className="text-xs text-danger">{error}</span>}
  </span>;
}
