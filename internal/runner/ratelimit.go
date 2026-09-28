package runner

import (
	"bytes"
	"encoding/json"
	"maps"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	RateLimitSourceStructured = "structured"
	RateLimitSourceRegex      = "regex"
	RateLimitSourceEstimate   = "estimate"

	FailureClassRateLimited = "rate_limited"

	minRateLimitBackoff = time.Minute
	maxRateLimitBackoff = time.Hour
	rateLimitTailBytes  = 64 << 10
)

// RateLimitResult describes a run that failed because the agent hit a vendor
// rate limit or spend cap. Backoff is set only for estimates; callers pass it
// back as prevBackoff when the next run is estimated too.
type RateLimitResult struct {
	Limited bool
	ResetAt time.Time
	Source  string
	Backoff time.Duration
}

// Classify reports whether a failed run's output lines show a rate limit and
// when it resets. Structured JSON fields win over vendor text; a detected limit
// without a reset time falls back to a capped exponential backoff estimate.
func Classify(lines [][]byte, exitCode int, now time.Time, prevBackoff time.Duration) (RateLimitResult, bool) {
	if exitCode == 0 {
		return RateLimitResult{}, false
	}
	var structured time.Time
	var regexReset time.Time
	detected := false
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		texts := [][]byte{line}
		if line[0] == '{' {
			var value any
			if err := json.Unmarshal(line, &value); err == nil {
				if resetAt, ok := structuredReset(value, now); ok {
					structured = resetAt
				}
				texts = eventTexts(value)
			}
		}
		for _, text := range texts {
			resetAt, limited := matchRateLimit(text, now)
			if !limited {
				continue
			}
			detected = true
			if !resetAt.IsZero() {
				regexReset = resetAt
			}
		}
	}
	switch {
	case !structured.IsZero():
		return RateLimitResult{Limited: true, ResetAt: structured, Source: RateLimitSourceStructured}, true
	case !regexReset.IsZero():
		return RateLimitResult{Limited: true, ResetAt: regexReset, Source: RateLimitSourceRegex}, true
	case detected:
		backoff := nextRateLimitBackoff(prevBackoff)
		return RateLimitResult{Limited: true, ResetAt: now.Add(backoff), Source: RateLimitSourceEstimate, Backoff: backoff}, true
	}
	return RateLimitResult{}, false
}

func nextRateLimitBackoff(previous time.Duration) time.Duration {
	if previous >= maxRateLimitBackoff/2 {
		return maxRateLimitBackoff
	}
	return min(max(previous*2, minRateLimitBackoff), maxRateLimitBackoff)
}

// eventTexts returns the top-level message strings of a JSON event. Nested
// content such as assistant text and tool output is ignored so that an agent
// reading or discussing rate-limit text is not mistaken for a limited run.
func eventTexts(value any) [][]byte {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	var texts [][]byte
	for _, key := range []string{"result", "error", "message"} {
		switch field := object[key].(type) {
		case string:
			texts = append(texts, []byte(field))
		case map[string]any:
			if message, ok := field["message"].(string); ok {
				texts = append(texts, []byte(message))
			}
		}
	}
	return texts
}

// structuredReset searches a decoded JSON value for resetsAt, retry-after or
// retryDelay fields. The last field found wins.
func structuredReset(value any, now time.Time) (time.Time, bool) {
	var found time.Time
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			if status, ok := typed["status"].(string); ok && strings.HasPrefix(strings.ToLower(status), "allowed") {
				return
			}
			for _, key := range slices.Sorted(maps.Keys(typed)) {
				field := typed[key]
				var resetAt time.Time
				var ok bool
				switch normalizeFieldName(key) {
				case "resetsat":
					resetAt, ok = parseResetInstant(field)
				case "retryafter":
					resetAt, ok = parseRetryAfter(field, now)
				case "retrydelay":
					resetAt, ok = parseRetryDelay(field, now)
				default:
					walk(field)
					continue
				}
				if ok {
					found = resetAt
				}
			}
		case []any:
			for _, item := range typed {
				walk(item)
			}
		}
	}
	walk(value)
	return found, !found.IsZero()
}

func normalizeFieldName(name string) string {
	return strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(name))
}

func parseResetInstant(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case float64:
		return unixReset(typed)
	case string:
		if number, err := strconv.ParseFloat(typed, 64); err == nil {
			return unixReset(number)
		}
		if instant, err := time.Parse(time.RFC3339, typed); err == nil {
			return instant.UTC(), true
		}
	}
	return time.Time{}, false
}

func unixReset(value float64) (time.Time, bool) {
	if value <= 0 || math.IsInf(value, 0) || math.IsNaN(value) {
		return time.Time{}, false
	}
	if value >= 1e12 {
		return time.UnixMilli(int64(value)).UTC(), true
	}
	return time.Unix(int64(value), 0).UTC(), true
}

func parseRetryAfter(value any, now time.Time) (time.Time, bool) {
	switch typed := value.(type) {
	case float64:
		return secondsAfter(now, typed)
	case string:
		if seconds, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
			return secondsAfter(now, seconds)
		}
		if instant, err := http.ParseTime(typed); err == nil {
			return instant.UTC(), true
		}
		return parseRetryDelay(typed, now)
	}
	return time.Time{}, false
}

func parseRetryDelay(value any, now time.Time) (time.Time, bool) {
	switch typed := value.(type) {
	case float64:
		return secondsAfter(now, typed)
	case string:
		if duration, ok := parseRelativeDuration(typed); ok {
			return now.Add(duration), true
		}
	case map[string]any:
		// google.protobuf.Duration encoded as an object.
		seconds, _ := typed["seconds"].(float64)
		nanos, _ := typed["nanos"].(float64)
		return secondsAfter(now, seconds+nanos/1e9)
	}
	return time.Time{}, false
}

func secondsAfter(now time.Time, seconds float64) (time.Time, bool) {
	if seconds < 0 || seconds > maxResetSeconds || math.IsNaN(seconds) {
		return time.Time{}, false
	}
	return now.Add(time.Duration(seconds * float64(time.Second))), true
}

// maxResetSeconds rejects absurd relative resets (more than a year).
const maxResetSeconds = 366 * 24 * 60 * 60

var durationPart = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(days?|d|hours?|hrs?|h|minutes?|mins?|ms|m|seconds?|secs?|s)`)

func parseRelativeDuration(text string) (time.Duration, bool) {
	parts := durationPart.FindAllStringSubmatch(text, -1)
	if len(parts) == 0 {
		return 0, false
	}
	var seconds float64
	for _, part := range parts {
		value, err := strconv.ParseFloat(part[1], 64)
		if err != nil {
			return 0, false
		}
		switch unit := strings.ToLower(part[2]); {
		case unit == "ms":
			seconds += value / 1000
		case strings.HasPrefix(unit, "d"):
			seconds += value * 24 * 60 * 60
		case strings.HasPrefix(unit, "h"):
			seconds += value * 60 * 60
		case strings.HasPrefix(unit, "m"):
			seconds += value * 60
		default:
			seconds += value
		}
	}
	if seconds > maxResetSeconds {
		return 0, false
	}
	return time.Duration(seconds * float64(time.Second)), true
}

// matchRateLimit applies the vendor table to one line. It reports whether a
// limit was detected and, when the matching pattern carries one, the reset.
func matchRateLimit(text []byte, now time.Time) (time.Time, bool) {
	detected := false
	for _, entry := range rateLimitPatterns {
		match := entry.pattern.FindSubmatch(text)
		if match == nil {
			continue
		}
		detected = true
		var resetAt time.Time
		var ok bool
		switch entry.kind {
		case resetEpoch:
			var seconds float64
			seconds, ok = parseFloat(match[1])
			if ok {
				resetAt, ok = unixReset(seconds)
			}
		case resetRelative:
			var duration time.Duration
			duration, ok = parseRelativeDuration(string(match[1]))
			resetAt = now.Add(duration)
		case resetClock:
			resetAt, ok = parseClockReset(string(match[1]), string(match[2]), now)
		}
		if ok {
			return resetAt, true
		}
	}
	return time.Time{}, detected
}

func parseFloat(value []byte) (float64, bool) {
	number, err := strconv.ParseFloat(string(value), 64)
	return number, err == nil
}

var clockParts = regexp.MustCompile(`(?i)^(?:([A-Z][a-z]{2,8})\.? (\d{1,2})(?:st|nd|rd|th)?,? (?:(\d{4}),? )?)?(\d{1,2})(?::(\d{2}))?\s*([ap])?\.?(?:m\.?)?$`)

// parseClockReset resolves a wall-clock reset such as "3pm", "3:05 PM" or
// "Oct 3rd, 2025 4:14 PM" in zone (or now's location) to an instant. Times
// without a date resolve to their next occurrence after now.
func parseClockReset(text, zone string, now time.Time) (time.Time, bool) {
	parts := clockParts.FindStringSubmatch(strings.TrimSpace(text))
	if parts == nil {
		return time.Time{}, false
	}
	location := now.Location()
	if zone != "" {
		if loaded, err := time.LoadLocation(zone); err == nil {
			location = loaded
		}
	}
	hour, _ := strconv.Atoi(parts[4])
	minute := 0
	if parts[5] != "" {
		minute, _ = strconv.Atoi(parts[5])
	}
	switch meridiem := strings.ToLower(parts[6]); {
	case meridiem == "" && parts[5] == "":
		// A bare number such as "resets 3" is ambiguous.
		return time.Time{}, false
	case meridiem != "" && (hour < 1 || hour > 12):
		return time.Time{}, false
	case meridiem == "a" && hour == 12:
		hour = 0
	case meridiem == "p" && hour != 12:
		hour += 12
	}
	if hour > 23 || minute > 59 {
		return time.Time{}, false
	}
	local := now.In(location)
	if parts[1] != "" {
		month, err := time.Parse("Jan", parts[1][:3])
		if err != nil {
			return time.Time{}, false
		}
		day, _ := strconv.Atoi(parts[2])
		year := local.Year()
		if parts[3] != "" {
			year, _ = strconv.Atoi(parts[3])
		}
		resetAt := time.Date(year, month.Month(), day, hour, minute, 0, 0, location)
		if parts[3] == "" && resetAt.Before(local.AddDate(0, -6, 0)) {
			resetAt = resetAt.AddDate(1, 0, 0)
		}
		return resetAt.UTC(), true
	}
	resetAt := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, location)
	if !resetAt.After(local) {
		resetAt = resetAt.AddDate(0, 0, 1)
	}
	return resetAt.UTC(), true
}

// tailBuffer keeps the last limit bytes written to it so that a failed run's
// final output can be classified without retaining the whole stream.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func newTailBuffer(limit int) *tailBuffer {
	return &tailBuffer{limit: limit}
}

func (buffer *tailBuffer) Write(chunk []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if len(chunk) >= buffer.limit {
		buffer.data = append(buffer.data[:0], chunk[len(chunk)-buffer.limit:]...)
		return len(chunk), nil
	}
	if overflow := len(buffer.data) + len(chunk) - buffer.limit; overflow > 0 {
		buffer.data = append(buffer.data[:0], buffer.data[overflow:]...)
	}
	buffer.data = append(buffer.data, chunk...)
	return len(chunk), nil
}

// lines splits the retained tail with LineReader. The first line may be a
// fragment when earlier output was discarded.
func (buffer *tailBuffer) lines() [][]byte {
	buffer.mu.Lock()
	data := bytes.Clone(buffer.data)
	buffer.mu.Unlock()
	var lines [][]byte
	for line, err := range LineReader(bytes.NewReader(data), 0) {
		if err != nil {
			break
		}
		lines = append(lines, line.Data)
	}
	return lines
}

func classifyRateLimit(result *Result, lines [][]byte, exitCode int, prevBackoff time.Duration) {
	limit, ok := Classify(lines, exitCode, time.Now().UTC(), prevBackoff)
	if !ok {
		return
	}
	resetAt := limit.ResetAt.UTC()
	result.FailureClass = FailureClassRateLimited
	result.ResetAt = &resetAt
	result.ResetSource = limit.Source
	result.RateLimitBackoffMillis = limit.Backoff.Milliseconds()
}
