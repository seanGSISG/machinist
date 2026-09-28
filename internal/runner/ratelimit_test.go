package runner

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
	_ "time/tzdata"
)

func TestClassifyRateLimit(t *testing.T) {
	now := time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		fixture string
		limited bool
		source  string
		resetAt time.Time
		backoff time.Duration
	}{
		{fixture: "claude_epoch.jsonl", limited: true, source: RateLimitSourceRegex, resetAt: time.Unix(1790000000, 0)},
		{fixture: "claude_structured.jsonl", limited: true, source: RateLimitSourceStructured, resetAt: time.Unix(1790003600, 0)},
		{fixture: "claude_resets_clock.txt", limited: true, source: RateLimitSourceRegex, resetAt: time.Date(2026, time.September, 27, 22, 0, 0, 0, time.UTC)},
		{fixture: "claude_weekly_clock.txt", limited: true, source: RateLimitSourceRegex, resetAt: time.Date(2026, time.October, 1, 8, 30, 0, 0, time.UTC)},
		{fixture: "claude_credit.jsonl", limited: true, source: RateLimitSourceEstimate, resetAt: now.Add(time.Minute), backoff: time.Minute},
		{fixture: "codex_relative.jsonl", limited: true, source: RateLimitSourceRegex, resetAt: now.Add(2*time.Hour + 5*time.Minute)},
		{fixture: "codex_absolute.jsonl", limited: true, source: RateLimitSourceRegex, resetAt: time.Date(2026, time.October, 3, 16, 14, 0, 0, time.UTC)},
		{fixture: "codex_clock.txt", limited: true, source: RateLimitSourceRegex, resetAt: time.Date(2026, time.September, 28, 8, 25, 0, 0, time.UTC)},
		{fixture: "codex_insufficient_quota.txt", limited: true, source: RateLimitSourceEstimate, resetAt: now.Add(time.Minute), backoff: time.Minute},
		{fixture: "gemini_retrydelay.jsonl", limited: true, source: RateLimitSourceStructured, resetAt: now.Add(32 * time.Second)},
		{fixture: "gemini_text.txt", limited: true, source: RateLimitSourceRegex, resetAt: now.Add(90*time.Second + 500*time.Millisecond)},
		{fixture: "gemini_reset_after.txt", limited: true, source: RateLimitSourceRegex, resetAt: now.Add(2*time.Hour + time.Minute + 3*time.Second)},
		{fixture: "gemini_daily_quota.txt", limited: true, source: RateLimitSourceEstimate, resetAt: now.Add(time.Minute), backoff: time.Minute},
		{fixture: "retry_after.jsonl", limited: true, source: RateLimitSourceStructured, resetAt: now.Add(2 * time.Minute)},
		{fixture: "failure_compile.txt"},
		{fixture: "failure_tool_output.jsonl"},
		{fixture: "failure_codex_stream.jsonl"},
	}
	entries, err := os.ReadDir(filepath.Join("testdata", "ratelimit"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(tests) {
		t.Fatalf("rate-limit fixtures = %d, want %d", len(entries), len(tests))
	}
	for _, test := range tests {
		t.Run(test.fixture, func(t *testing.T) {
			lines := readRateLimitFixture(t, test.fixture)
			got, ok := Classify(lines, 1, now, 0)
			if ok != test.limited || got.Limited != test.limited {
				t.Fatalf("Classify() = %+v, %v; want limited %v", got, ok, test.limited)
			}
			if !ok {
				if got != (RateLimitResult{}) {
					t.Fatalf("non-limited result = %+v, want zero", got)
				}
				return
			}
			if got.Source != test.source || !got.ResetAt.Equal(test.resetAt) || got.Backoff != test.backoff {
				t.Fatalf("Classify() = source %q reset %s backoff %s; want source %q reset %s backoff %s", got.Source, got.ResetAt.UTC(), got.Backoff, test.source, test.resetAt.UTC(), test.backoff)
			}
			if _, ok := Classify(lines, 0, now, 0); ok {
				t.Fatal("successful exit classified as rate limited")
			}
		})
	}

	t.Run("estimate backoff", func(t *testing.T) {
		lines := readRateLimitFixture(t, "claude_credit.jsonl")
		for _, test := range []struct {
			previous time.Duration
			want     time.Duration
		}{
			{previous: 0, want: time.Minute},
			{previous: 20 * time.Second, want: time.Minute},
			{previous: time.Minute, want: 2 * time.Minute},
			{previous: 16 * time.Minute, want: 32 * time.Minute},
			{previous: 32 * time.Minute, want: time.Hour},
			{previous: time.Hour, want: time.Hour},
			{previous: 1 << 62, want: time.Hour},
		} {
			got, ok := Classify(lines, 1, now, test.previous)
			if !ok || got.Source != RateLimitSourceEstimate || got.Backoff != test.want || !got.ResetAt.Equal(now.Add(test.want)) {
				t.Fatalf("Classify(prev=%s) = %+v, %v; want estimate backoff %s", test.previous, got, ok, test.want)
			}
		}
	})
}

func TestExecuteClassifiesRateLimitedFailure(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
		class  string
		source string
	}{
		{name: "estimate", script: `cat >/dev/null; printf 'Credit balance is too low\n' >&2; exit 1`, class: FailureClassRateLimited, source: RateLimitSourceEstimate},
		{name: "regex", script: `cat >/dev/null; printf '{"type":"error","message":"You have hit your usage limit"}\n{"type":"turn.failed","error":{"message":"rate limit reached, try again in 5 minutes"}}\n'; exit 1`, class: FailureClassRateLimited, source: RateLimitSourceRegex},
		{name: "other failure", script: `cat >/dev/null; printf 'build failed\n'; exit 1`},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := helperAgent("fail", 5*time.Second)
			command.Command = []string{"/bin/sh", "-c", test.script}
			started := time.Now()
			result, err := Execute(t.Context(), Options{
				Command:          command,
				Repository:       newGitRepository(t),
				DataDirectory:    t.TempDir(),
				Stdout:           io.Discard,
				Stderr:           io.Discard,
				RateLimitBackoff: 2 * time.Minute,
			})
			if err == nil || result.State != StateFailed {
				t.Fatalf("Execute() = %#v, %v", result, err)
			}
			if result.FailureClass != test.class || result.ResetSource != test.source {
				t.Fatalf("failure class = %q source = %q", result.FailureClass, result.ResetSource)
			}
			if test.class == "" {
				if result.ResetAt != nil || result.RateLimitBackoffMillis != 0 {
					t.Fatalf("non-limited result = %#v", result)
				}
				return
			}
			if result.ResetAt == nil || result.ResetAt.Before(started) {
				t.Fatalf("reset at = %v", result.ResetAt)
			}
			if test.source == RateLimitSourceEstimate && result.RateLimitBackoffMillis != (4*time.Minute).Milliseconds() {
				t.Fatalf("backoff millis = %d", result.RateLimitBackoffMillis)
			}
			body, err := os.ReadFile(filepath.Join(filepath.Dir(result.EventsPath), "result.json"))
			if err != nil {
				t.Fatal(err)
			}
			var persisted Result
			if err := json.Unmarshal(body, &persisted); err != nil {
				t.Fatal(err)
			}
			if persisted.FailureClass != test.class || persisted.ResetSource != test.source || persisted.ResetAt == nil || !persisted.ResetAt.Equal(*result.ResetAt) {
				t.Fatalf("persisted result = %s", body)
			}
		})
	}
}

func TestTailBufferKeepsFinalLines(t *testing.T) {
	buffer := newTailBuffer(16)
	for _, chunk := range []string{"first line\n", "second\n", "limit reached\n"} {
		if _, err := buffer.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	lines := buffer.lines()
	if len(lines) == 0 || string(lines[len(lines)-1]) != "limit reached" {
		t.Fatalf("tail lines = %q", lines)
	}
	if len(buffer.data) > 16 {
		t.Fatalf("tail retained %d bytes, want <= 16", len(buffer.data))
	}
}

func readRateLimitFixture(t *testing.T, name string) [][]byte {
	t.Helper()
	file, err := os.Open(filepath.Join("testdata", "ratelimit", name))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var lines [][]byte
	for line, err := range LineReader(file, 0) {
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line.Data)
	}
	return lines
}
