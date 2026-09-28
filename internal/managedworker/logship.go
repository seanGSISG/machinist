package managedworker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
)

const (
	logShipInterval  = time.Second
	maxLogChunkBytes = 16 << 10
)

// shipLogs sends snapshots independently of the heartbeat loop. Closing stop
// requests one final shipment before the goroutine exits.
func (w *Worker) shipLogs(ctx context.Context, spec protocol.RunSpec, ring *runner.LogRing, stop <-chan struct{}) {
	ticks := w.logTicks
	if ticks == nil {
		ticker := time.NewTicker(logShipInterval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	offset := int64(0)
	ship := func() {
		chunk, next, truncated := ring.Since(offset)
		if len(chunk) == 0 && !truncated {
			return
		}
		chunkOffset := offset
		if len(chunk) > maxLogChunkBytes {
			chunk = chunk[len(chunk)-maxLogChunkBytes:]
			chunkOffset = next - int64(len(chunk))
			truncated = true
		} else if truncated {
			chunkOffset = next - int64(len(chunk))
		}
		input := protocol.LogChunk{Offset: chunkOffset, Data: chunk, Truncated: truncated}
		if err := w.client.postRunLog(ctx, spec.ID, w.instanceID, spec.LeaseToken, input); err != nil {
			if ctx.Err() == nil && w.stderr != nil {
				fmt.Fprintf(w.stderr, "machinist: ship log for run %s: %v\n", spec.ID, err)
			}
			return
		}
		offset = next
	}

	for {
		select {
		case <-ticks:
			ship()
		case <-stop:
			ship()
			return
		case <-ctx.Done():
			return
		}
	}
}

func (client *Client) postRunLog(ctx context.Context, runID, instanceID, leaseToken string, input protocol.LogChunk) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.base+"/api/v1/runs/"+url.PathEscape(runID)+"/log", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Machinist-Instance", instanceID)
	request.Header.Set("X-Machinist-Lease", leaseToken)
	response, err := client.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	message, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
	return &ResponseError{Status: response.StatusCode, Body: string(bytes.TrimSpace(message))}
}
