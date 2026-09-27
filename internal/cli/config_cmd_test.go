package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigValidateCmd(t *testing.T) {
	directory := t.TempDir()
	validPath := filepath.Join(directory, "valid.toml")
	invalidPath := filepath.Join(directory, "invalid.toml")
	syntaxPath := filepath.Join(directory, "syntax.toml")
	if err := os.WriteFile(validPath, []byte("[commands.good]\nexecutor = \"test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(invalidPath, []byte("[commands.bad]\nexecutor = 42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(syntaxPath, []byte("[commands.bad\nexecutor = \"test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name      string
		path      string
		json      bool
		wantCode  int
		wantValid bool
	}{
		{name: "valid human", path: validPath, wantCode: 0, wantValid: true},
		{name: "invalid human", path: invalidPath, wantCode: 1},
		{name: "valid JSON", path: validPath, json: true, wantCode: 0, wantValid: true},
		{name: "invalid JSON", path: invalidPath, json: true, wantCode: 1},
		{name: "syntax JSON", path: syntaxPath, json: true, wantCode: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"config", "validate", "--config", test.path}
			if test.json {
				args = append(args, "--json")
			}
			var stdout, stderr bytes.Buffer
			code := Execute(t.Context(), args, strings.NewReader(""), &stdout, &stderr, "test")
			if code != test.wantCode {
				t.Fatalf("exit code = %d, want %d; stdout = %q, stderr = %q", code, test.wantCode, stdout.String(), stderr.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q", stderr.String())
			}
			if !test.json {
				want := "configuration is invalid"
				if test.wantValid {
					want = "configuration is valid"
				}
				if !strings.Contains(stdout.String(), want) {
					t.Fatalf("stdout = %q", stdout.String())
				}
				return
			}
			var result struct {
				Valid           bool             `json:"valid"`
				InvalidCommands []map[string]any `json:"invalid_commands"`
				Error           *string          `json:"error"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatalf("decode JSON %q: %v", stdout.String(), err)
			}
			if result.Valid != test.wantValid || result.InvalidCommands == nil {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}
