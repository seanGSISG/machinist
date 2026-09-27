package config

import "testing"

func TestNormalizeOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"https://Machinist.Lab.Example":   "https://machinist.lab.example",
		"https://machinist.lab.example/":  "https://machinist.lab.example",
		"http://colo.example.ts.net:8444": "http://colo.example.ts.net:8444",
		" https://machinist.lab.example ": "https://machinist.lab.example",
	} {
		if got, err := NormalizeOrigin(in); err != nil || got != want {
			t.Errorf("NormalizeOrigin(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"machinist.lab.example", "ftp://x", "https://x/path", "https://user@x", "https://x?q=1", "https://"} {
		if _, err := NormalizeOrigin(bad); err == nil {
			t.Errorf("NormalizeOrigin(%q) accepted", bad)
		}
	}
}
