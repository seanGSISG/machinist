package runner

import "regexp"

// resetKind describes how a vendor rate-limit pattern encodes its reset time.
type resetKind int

const (
	// resetNone only detects a limit; the reset time must be estimated.
	resetNone resetKind = iota
	// resetEpoch captures a Unix timestamp in seconds.
	resetEpoch
	// resetRelative captures a duration such as "2 hours 5 minutes" or "1m30s".
	resetRelative
	// resetClock captures a wall-clock time with an optional date and an
	// optional parenthesized IANA time zone.
	resetClock
)

type rateLimitPattern struct {
	vendor  string
	kind    resetKind
	pattern *regexp.Regexp
}

const (
	relativeDurationPattern = `(\d+(?:\.\d+)?\s*(?:days?|d|hours?|hrs?|h|minutes?|mins?|ms|m|seconds?|secs?|s)(?:[\s,]*(?:and\s+)?\d+(?:\.\d+)?\s*(?:days?|d|hours?|hrs?|h|minutes?|mins?|ms|m|seconds?|secs?|s))*)\b`
	clockPattern            = `((?:[A-Z][a-z]{2,8}\.? \d{1,2}(?:st|nd|rd|th)?,? (?:\d{4},? )?)?\d{1,2}(?::\d{2})?\s*(?:[ap]\.?m\.?)?)(?:\s*\(([A-Za-z_]+(?:/[A-Za-z0-9_+\-]+)*)\))?`
)

// rateLimitPatterns is ordered from most to least specific. Patterns that
// carry a reset time come before detect-only patterns so that the first
// matching pattern on a line supplies the best available reset estimate.
var rateLimitPatterns = []rateLimitPattern{
	// Claude Code: "Claude AI usage limit reached|1759000000".
	{vendor: "claude", kind: resetEpoch, pattern: regexp.MustCompile(`(?i)claude ai usage limit reached\|(\d{9,11})`)},
	// Claude Code: "5-hour limit reached ∙ resets 3pm (America/Los_Angeles)",
	// "You've hit your limit · resets 3:30pm", "Your limit will reset at 3pm".
	{vendor: "claude", kind: resetClock, pattern: regexp.MustCompile(`(?i)(?:limit reached|hit your (?:usage )?limit|usage limit)[^\n]{0,40}?\b(?:resets|limit will reset)(?: at)? ` + clockPattern)},
	// Codex: "You've hit your usage limit. ... or try again in 2 hours 5 minutes."
	{vendor: "codex", kind: resetRelative, pattern: regexp.MustCompile(`(?i)(?:usage limit|rate limit)[^\n]{0,240}?\btry again in ` + relativeDurationPattern)},
	// Codex: "You've hit your usage limit. ... or try again at 3:05 PM."
	{vendor: "codex", kind: resetClock, pattern: regexp.MustCompile(`(?i)usage limit[^\n]{0,240}?\btry again at ` + clockPattern)},
	// Gemini: "Quota exceeded for quota metric ... Please retry in 32.5s" and
	// "Your quota will reset after 2h1m3s."
	{vendor: "gemini", kind: resetRelative, pattern: regexp.MustCompile(`(?i)(?:quota|resource_exhausted|rate limit)[^\n]{0,400}?\b(?:retry in|reset after|resets? in) ` + relativeDurationPattern)},
	// Detect-only phrasings: limit or spend cap without a parseable reset.
	{vendor: "claude", kind: resetNone, pattern: regexp.MustCompile(`(?i)claude ai usage limit reached|credit balance is too low|(?:5-hour|weekly|opus|session) limit reached|you've hit your (?:usage )?limit`)},
	{vendor: "codex", kind: resetNone, pattern: regexp.MustCompile(`(?i)you've hit your usage limit|usage_limit_reached|usage limit has been reached|insufficient_quota|exceeded your current quota|rate_limit_exceeded`)},
	{vendor: "gemini", kind: resetNone, pattern: regexp.MustCompile(`(?i)resource_exhausted|quota exceeded for quota metric|exhausted your (?:daily )?(?:quota|capacity)|quota will reset`)},
	{vendor: "generic", kind: resetNone, pattern: regexp.MustCompile(`(?i)\b429 too many requests\b|\brate limit(?:ed| exceeded| reached)\b|\bspend(?:ing)? (?:limit|cap) (?:reached|exceeded)\b`)},
}
