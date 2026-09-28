package runner

import (
	"bufio"
	"errors"
	"io"
	"iter"
)

const defaultMaxLine = 1 << 20

// Line is one line read from a stream. Data does not include the trailing
// newline. Truncated reports that bytes beyond the configured cap were
// discarded.
type Line struct {
	Data      []byte
	Truncated bool
}

// LineReader yields capped lines from r. A non-positive maxLine selects the
// one MiB default. Read failures are yielded with a zero Line and stop the
// sequence; io.EOF is not reported as an error.
func LineReader(r io.Reader, maxLine int) iter.Seq2[Line, error] {
	if maxLine <= 0 {
		maxLine = defaultMaxLine
	}
	return func(yield func(Line, error) bool) {
		reader := bufio.NewReader(r)
		line := make([]byte, 0, min(maxLine, bufio.MaxScanTokenSize))
		truncated := false
		for {
			fragment, err := reader.ReadSlice('\n')
			terminated := len(fragment) > 0 && fragment[len(fragment)-1] == '\n'
			if terminated {
				fragment = fragment[:len(fragment)-1]
			}
			if !truncated {
				remaining := maxLine - len(line)
				if len(fragment) > remaining {
					line = append(line, fragment[:remaining]...)
					truncated = true
				} else {
					line = append(line, fragment...)
				}
			}

			if terminated {
				if !yield(Line{Data: line, Truncated: truncated}, nil) {
					return
				}
				line = make([]byte, 0, min(maxLine, bufio.MaxScanTokenSize))
				truncated = false
			}

			switch {
			case err == nil, errors.Is(err, bufio.ErrBufferFull):
				continue
			case errors.Is(err, io.EOF):
				if !terminated && (len(line) > 0 || truncated) {
					yield(Line{Data: line, Truncated: truncated}, nil)
				}
				return
			default:
				yield(Line{}, err)
				return
			}
		}
	}
}
