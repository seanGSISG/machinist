package runner

import (
	"bytes"
	"testing"
)

func TestLogRing(t *testing.T) {
	ring := NewLogRing(8)
	if _, err := ring.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	chunk, next, truncated := ring.Since(0)
	if string(chunk) != "abc" || next != 3 || truncated {
		t.Fatalf("first read = %q, %d, %v", chunk, next, truncated)
	}

	if _, err := ring.Write([]byte("defghijk")); err != nil {
		t.Fatal(err)
	}
	chunk, next, truncated = ring.Since(3)
	if string(chunk) != "defghijk" || next != 11 || truncated {
		t.Fatalf("exact retained read = %q, %d, %v", chunk, next, truncated)
	}
	chunk, next, truncated = ring.Since(0)
	if string(chunk) != "defghijk" || next != 11 || !truncated {
		t.Fatalf("truncated read = %q, %d, %v", chunk, next, truncated)
	}

	chunk[0] = 'X'
	again, _, _ := ring.Since(3)
	if bytes.Equal(chunk, again) || string(again) != "defghijk" {
		t.Fatalf("Since returned aliased data: %q", again)
	}
	if written, err := ring.Write([]byte("0123456789")); written != 10 || err != nil {
		t.Fatalf("large write = %d, %v", written, err)
	}
	chunk, next, truncated = ring.Since(11)
	if string(chunk) != "23456789" || next != 21 || !truncated {
		t.Fatalf("large write read = %q, %d, %v", chunk, next, truncated)
	}
}
