package managedworker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
)

func TestLogShip(t *testing.T) {
	logRequest := make(chan protocol.LogChunk, 1)
	heartbeat := make(chan struct{}, 1)
	releaseLog := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		switch request.URL.Path {
		case "/api/v1/runs/run-test/log":
			if request.Header.Get("X-Machinist-Instance") != "worker-test" || request.Header.Get("X-Machinist-Lease") != "lease-test" {
				t.Errorf("lease headers = %q, %q", request.Header.Get("X-Machinist-Instance"), request.Header.Get("X-Machinist-Lease"))
			}
			var chunk protocol.LogChunk
			if err := json.NewDecoder(request.Body).Decode(&chunk); err != nil {
				t.Errorf("decode log: %v", err)
			}
			logRequest <- chunk
			<-releaseLog
			response.WriteHeader(http.StatusNoContent)
		case "/api/v1/runs/run-test/heartbeat":
			heartbeat <- struct{}{}
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	ticks := make(chan time.Time, 1)
	worker := &Worker{
		instanceID: "worker-test",
		client:     newClient(server.URL, "secret", server.Client()),
		stderr:     io.Discard,
		logTicks:   ticks,
	}
	ring := runner.NewLogRing(64 << 10)
	input := bytes.Repeat([]byte("x"), maxLogChunkBytes+123)
	_, _ = ring.Write(input)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		worker.shipLogs(context.Background(), protocol.RunSpec{ID: "run-test", LeaseToken: "lease-test"}, ring, stop)
		close(done)
	}()
	ticks <- time.Time{}
	chunk := <-logRequest
	if len(chunk.Data) != maxLogChunkBytes || !chunk.Truncated || chunk.Offset != 123 || !bytes.Equal(chunk.Data, input[123:]) {
		t.Fatalf("chunk = offset %d, bytes %d, truncated %v", chunk.Offset, len(chunk.Data), chunk.Truncated)
	}

	heartbeatDone := make(chan error, 1)
	go func() {
		heartbeatDone <- worker.heartbeat(t.Context(), protocol.RunSpec{ID: "run-test", LeaseToken: "lease-test"})
	}()
	select {
	case <-heartbeat:
	case <-time.After(time.Second):
		t.Fatal("heartbeat was delayed by blocked log request")
	}
	if err := <-heartbeatDone; err != nil {
		t.Fatal(err)
	}
	close(releaseLog)
	close(stop)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("log shipper did not stop")
	}
}
