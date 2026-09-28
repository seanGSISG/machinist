const groups = ["gates_awaiting_approval", "blocked_jobs", "logins"];

export function needsMe(status = {}) {
  return groups.flatMap((group) => Array.isArray(status[group]) ? status[group] : []);
}

export function titleFor(count) {
  return count > 0 ? `(${count}) Machinist` : "Machinist";
}

export function relativeAge(since, now = new Date()) {
  const value = since instanceof Date ? since : new Date(since);
  const current = now instanceof Date ? now : new Date(now);
  const milliseconds = value.getTime() - current.getTime();
  if (!Number.isFinite(milliseconds)) return "";
  const absolute = Math.abs(milliseconds);
  let divisor = 1000;
  let unit = "second";
  if (absolute >= 24 * 60 * 60 * 1000) {
    divisor = 24 * 60 * 60 * 1000;
    unit = "day";
  } else if (absolute >= 60 * 60 * 1000) {
    divisor = 60 * 60 * 1000;
    unit = "hour";
  } else if (absolute >= 60 * 1000) {
    divisor = 60 * 1000;
    unit = "minute";
  }
  return new Intl.RelativeTimeFormat(undefined, { numeric: "auto" }).format(Math.round(milliseconds / divisor), unit);
}

export function attentionTone(reason) {
  if (reason === "GateAwaitingApproval") return { icon: "approval", text: "Approval needed" };
  if (reason === "Blocked") return { icon: "blocked", text: "Blocked" };
  if (reason === "LoginExpired") return { icon: "login", text: "Login expired" };
  if (reason === "LoginRechecking") return { icon: "login", text: "Login rechecking" };
  return { icon: "attention", text: "Needs attention" };
}
