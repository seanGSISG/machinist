package runner

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
)

var updateUsageGoldens = flag.Bool("update", false, "update usage stream golden fixtures")

func TestUsageShape(t *testing.T) {
	fixtureDirectory := filepath.Join("testdata", "streams")
	if *updateUsageGoldens {
		writeUsageFixtures(t, fixtureDirectory)
	}
	paths, err := filepath.Glob(filepath.Join(fixtureDirectory, "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 4 {
		t.Fatalf("stream fixtures = %d, want 4", len(paths))
	}
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		t.Run(name, func(t *testing.T) {
			stream, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			var got protocol.Usage
			var found bool
			for line, err := range LineReader(stream, 0) {
				if err != nil {
					t.Fatal(err)
				}
				if line.Truncated {
					continue
				}
				if usage, ok := Detect(line.Data); ok {
					got, found = usage, true
				}
			}
			if !found {
				t.Fatal("usage not detected")
			}
			expectedBody, err := os.ReadFile(filepath.Join(fixtureDirectory, name+".usage.json"))
			if err != nil {
				t.Fatal(err)
			}
			var expected protocol.Usage
			if err := json.Unmarshal(expectedBody, &expected); err != nil {
				t.Fatal(err)
			}
			if got != expected {
				t.Fatalf("usage = %#v, want %#v", got, expected)
			}
		})
	}
}

func TestUsageShapeClaudeMessageDelta(t *testing.T) {
	usage, ok := Detect([]byte(`{"type":"message_delta","usage":{"output_tokens":17}}`))
	if !ok || usage != (protocol.Usage{OutputTokens: 17}) {
		t.Fatalf("usage = %#v, detected = %v", usage, ok)
	}
}

func TestUsageShapeRejectsMalformedKnownEvents(t *testing.T) {
	for _, line := range []string{
		`{"type":"turn.completed","usage":{"input_tokens":1}}`,
		`{"type":"turn.completed","usage":{"input_tokens":-1,"output_tokens":2}}`,
		`{"type":"result","usage":{"input_tokens":1,"cache_creation_input_tokens":2,"cache_read_input_tokens":3}}`,
		`{"type":"other","usage":{"input_tokens":1,"output_tokens":2}}`,
	} {
		if usage, ok := Detect([]byte(line)); ok {
			t.Fatalf("Detect(%s) = %#v, true", line, usage)
		}
	}
}

func writeUsageFixtures(t *testing.T, directory string) {
	t.Helper()
	fixtures := map[string][]byte{
		"claude": []byte("{\"type\":\"system\",\"subtype\":\"init\"}\n" +
			"{\"type\":\"result\",\"model\":\"claude-sonnet\",\"usage\":{\"input_tokens\":100,\"cache_creation_input_tokens\":20,\"cache_read_input_tokens\":30,\"output_tokens\":4}}\n"),
		"codex": []byte("{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"done\"}}\n" +
			"{\"type\":\"turn.completed\",\"model\":\"gpt-5\",\"usage\":{\"input_tokens\":40,\"cached_input_tokens\":35,\"output_tokens\":3,\"reasoning_tokens\":2}}\n"),
		"agent-run-contract": []byte("{\"wrapper\":\"agent-run --contract\"}\n" +
			"{\"type\":\"turn.completed\",\"model\":\"gpt-5-codex\",\"usage\":{\"input_tokens\":12,\"cached_input_tokens\":5,\"output_tokens\":8}}\n"),
		"long-line": bytes.Join([][]byte{
			[]byte(`{"type":"diagnostic","message":"`),
			bytes.Repeat([]byte("x"), 70<<10),
			[]byte("\"}\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":9,\"output_tokens\":6}}\n"),
		}, nil),
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, stream := range fixtures {
		if err := os.WriteFile(filepath.Join(directory, name+".jsonl"), stream, 0o600); err != nil {
			t.Fatal(err)
		}
		var usage protocol.Usage
		for line, err := range LineReader(bytes.NewReader(stream), 0) {
			if err != nil {
				t.Fatal(err)
			}
			if detected, ok := Detect(line.Data); ok {
				usage = detected
			}
		}
		expected, err := json.MarshalIndent(usage, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		expected = append(expected, '\n')
		if err := os.WriteFile(filepath.Join(directory, name+".usage.json"), expected, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
