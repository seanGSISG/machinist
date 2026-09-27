const pages = new Set(["runs", "analytics", "workers", "triggers", "commands", "workflows", "settings", "connections"]);

export function routeFromHash(hash) {
  const value = hash.replace(/^#\//, "");
  if (value.startsWith("runs/") && value.slice(5)) {
    try {
      return { view: "task", jobID: decodeURIComponent(value.slice(5)) };
    } catch {
      return { view: "runs", jobID: "" };
    }
  }
  if (value.startsWith("run/") && value.slice(4)) {
    try {
      return { view: "run", runID: decodeURIComponent(value.slice(4)) };
    } catch {
      return { view: "runs", jobID: "" };
    }
  }
  if (value.startsWith("gate/") && value.slice(5)) {
    try {
      return { view: "gate", jobID: decodeURIComponent(value.slice(5)) };
    } catch {
      return { view: "runs", jobID: "" };
    }
  }
  if (value === "usage") return { view: "usage" };
  return { view: pages.has(value) ? value : "runs", jobID: "" };
}
