export const noTicketLabel = "No ticket";

// Most recent activity for a job: its last update, falling back to creation.
export function jobActivity(job) {
  const time = Date.parse(job.updated_at || job.created_at || "");
  return Number.isNaN(time) ? 0 : time;
}

export function visibleJobs(jobs, { showSuperseded = false } = {}) {
  if (showSuperseded) return jobs;
  return jobs.filter((job) => !(job.superseded && job.state === "cancelled"));
}

// groupJobs returns [{ key, label, jobs }] ordered by each group's most recent
// activity. Mode "none" yields one group holding every job in input order.
export function groupJobs(jobs, mode) {
  if (mode !== "ticket") return jobs.length ? [{ key: "all", label: "", jobs }] : [];
  const groups = new Map();
  for (const job of jobs) {
    const ticket = typeof job.labels?.ticket === "string" ? job.labels.ticket.trim() : "";
    const key = ticket ? `ticket:${ticket}` : "none";
    if (!groups.has(key)) groups.set(key, { key, label: ticket || noTicketLabel, jobs: [], activity: 0 });
    const group = groups.get(key);
    group.jobs.push(job);
    group.activity = Math.max(group.activity, jobActivity(job));
  }
  return [...groups.values()]
    .sort((a, b) => b.activity - a.activity || a.label.localeCompare(b.label))
    .map(({ key, label, jobs: grouped }) => ({ key, label, jobs: grouped }));
}
