package runner

import (
	"errors"
	"strings"
	"testing"
)

func TestLineReader(t *testing.T) {
	input := "short\n" + strings.Repeat("x", 12) + "\nafter\ntail"
	var got []Line
	for line, err := range LineReader(strings.NewReader(input), 8) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, line)
	}
	want := []Line{
		{Data: []byte("short")},
		{Data: []byte("xxxxxxxx"), Truncated: true},
		{Data: []byte("after")},
		{Data: []byte("tail")},
	}
	if len(got) != len(want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
	for index := range want {
		if string(got[index].Data) != string(want[index].Data) || got[index].Truncated != want[index].Truncated {
			t.Fatalf("line %d = %#v, want %#v", index, got[index], want[index])
		}
	}
}

func TestLineReaderUsesDefaultCapAndContinues(t *testing.T) {
	input := strings.Repeat("a", defaultMaxLine+1) + "\nok\n"
	var got []Line
	for line, err := range LineReader(strings.NewReader(input), 0) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, line)
	}
	if len(got) != 2 || len(got[0].Data) != defaultMaxLine || !got[0].Truncated || string(got[1].Data) != "ok" || got[1].Truncated {
		t.Fatalf("lines = %#v", got)
	}
}

type failingLineReader struct {
	read bool
}

func (reader *failingLineReader) Read(buffer []byte) (int, error) {
	if !reader.read {
		reader.read = true
		copy(buffer, "partial")
		return len("partial"), nil
	}
	return 0, errors.New("read failed")
}

func TestLineReaderReportsReadFailure(t *testing.T) {
	var got error
	for _, err := range LineReader(&failingLineReader{}, 8) {
		got = err
	}
	if got == nil || got.Error() != "read failed" {
		t.Fatalf("error = %v", got)
	}
}
