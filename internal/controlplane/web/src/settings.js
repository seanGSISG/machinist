export const PROMPT_WARN_BYTES = 2048;
export const PROMPT_WARN_LINES = 40;

// Advisory only: large prompts usually mean a step should be split.
export function promptWarnings(prompt = "") {
  const warnings = [];
  const bytes = new TextEncoder().encode(prompt).length;
  if (bytes > PROMPT_WARN_BYTES) warnings.push(`Prompt is ${bytes} bytes; keep prompts under about ${PROMPT_WARN_BYTES} bytes.`);
  const lines = prompt.split("\n").filter((line) => line.trim() !== "").length;
  if (lines > PROMPT_WARN_LINES) warnings.push(`Prompt has ${lines} non-empty lines; keep prompts under about ${PROMPT_WARN_LINES} instructions.`);
  return warnings;
}

export function commandForm(view) {
  const override = view?.override || {};
  return { executor: override.executor || "", model: override.model || "", timeout: override.timeout || "", prompt: override.prompt ?? "" };
}

// Empty fields inherit from config.toml; an all-empty form clears the override.
export function commandOverride(form) {
  const value = {};
  for (const field of ["executor", "model", "timeout"]) if (form[field].trim()) value[field] = form[field].trim();
  if (form.prompt.trim()) value.prompt = form.prompt;
  return Object.keys(value).length ? value : null;
}

export function workflowSteps(view) {
  return (view?.effective || view?.override || view?.file || { steps: [] }).steps.map((step) => ({ ...step, approval: Boolean(step.approval) }));
}

export function workflowOverride(steps) {
  return { steps: steps.map(({ command, approval, id, required_outputs }) => ({ command, ...(approval ? { approval: true } : {}), ...(id ? { id } : {}), ...(required_outputs?.length ? { required_outputs } : {}) })) };
}

export function moveStep(steps, index, delta) {
  const target = index + delta;
  if (target < 0 || target >= steps.length) return steps;
  const next = [...steps];
  [next[index], next[target]] = [next[target], next[index]];
  return next;
}

export function describeVersion(version) {
  if (version.value === null) return "Override removed; config.toml applies";
  if (version.reverted_from) return `Reverted to version ${version.reverted_from}`;
  return "Saved";
}

export function sourceLabel(view) {
  if (!view.override) return "config.toml";
  return view.file ? "Overridden" : "Created in UI";
}

export async function settingsRequest(path, { method = "GET", body, csrfToken } = {}) {
  const response = await fetch(path, {
    method,
    headers: { Accept: "application/json", ...(body !== undefined ? { "Content-Type": "application/json" } : {}), ...(method !== "GET" ? { "X-Machinist-CSRF": csrfToken || "" } : {}) },
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
  });
  const result = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(result.error || `Settings request failed (${response.status})`);
  return result;
}

// Save confirmations outlive the editor remount that the post-save reload causes (editors are keyed by version).
const saveNotices = new Map();
export function rememberNotice(key, message) {
  saveNotices.set(key, message);
}
export function takeNotice(key) {
  const message = saveNotices.get(key) ?? null;
  saveNotices.delete(key);
  return message;
}

// An executor's default model can be set only while a worker offers model selection, but a stored
// default must stay clearable after the executor goes offline or stops supporting models.
export function executorSaveMode(executor) {
  const unavailable = executor.workers.length === 0 || !executor.supports_model;
  if (!unavailable) return "save";
  return executor.override ? "clear" : "none";
}
