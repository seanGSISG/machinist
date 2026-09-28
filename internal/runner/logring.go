package runner

import "sync"

const defaultLogRingBytes = 256 << 10

// LogRing retains a bounded tail of a byte stream and assigns every written
// byte a monotonically increasing offset. It is safe for concurrent use.
type LogRing struct {
	mu       sync.Mutex
	data     []byte
	capacity int
	next     int64
}

// NewLogRing constructs a log ring with the requested byte capacity. A
// non-positive capacity selects the 256 KiB default.
func NewLogRing(capacity int) *LogRing {
	if capacity <= 0 {
		capacity = defaultLogRingBytes
	}
	return &LogRing{capacity: capacity}
}

// Write adds bytes to the ring. It always consumes the complete input,
// retaining only the newest bytes that fit.
func (ring *LogRing) Write(body []byte) (int, error) {
	ring.mu.Lock()
	defer ring.mu.Unlock()
	ring.initialize()

	ring.next += int64(len(body))
	if len(body) >= ring.capacity {
		ring.data = append(ring.data[:0], body[len(body)-ring.capacity:]...)
		return len(body), nil
	}
	overflow := len(ring.data) + len(body) - ring.capacity
	if overflow > 0 {
		copy(ring.data, ring.data[overflow:])
		ring.data = ring.data[:len(ring.data)-overflow]
	}
	ring.data = append(ring.data, body...)
	return len(body), nil
}

// Since returns the retained bytes at or after offset, the stream's next byte
// offset, and whether bytes between the requested offset and returned data are
// no longer available.
func (ring *LogRing) Since(offset int64) (chunk []byte, next int64, truncated bool) {
	ring.mu.Lock()
	defer ring.mu.Unlock()
	ring.initialize()

	start := ring.next - int64(len(ring.data))
	switch {
	case offset < start:
		offset = start
		truncated = true
	case offset > ring.next:
		return nil, ring.next, true
	}
	chunk = append([]byte(nil), ring.data[int(offset-start):]...)
	return chunk, ring.next, truncated
}

func (ring *LogRing) initialize() {
	if ring.capacity <= 0 {
		ring.capacity = defaultLogRingBytes
	}
}
