package controlplane

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

func TestRunLogOffsets(t *testing.T) {
	server, web := newTestHTTPServer(t)
	defer web.Close()
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	server.store.now = func() time.Time { return now }
	_, err := server.store.db.Exec(`INSERT INTO jobs(id,prompt,repository,command,state,created_at,updated_at) VALUES('job-log','prompt','repo','plan','running',?,?)`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.db.Exec(`INSERT INTO runs(id,job_id,command,command_hash,executor,repository,rendered_prompt,timeout_ms,state,worker_instance,lease_token,lease_expires_at) VALUES('run-log','job-log','plan','hash','test','repo','prompt',1000,'running','worker-log','lease-log',?)`, now.Add(time.Minute).UnixNano()); err != nil {
		t.Fatal(err)
	}

	unauthorized := postRunLog(t, web.URL, protocol.LogChunk{Data: []byte("no")}, nil)
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.StatusCode)
	}
	unauthorized.Body.Close()
	badLease := postRunLog(t, web.URL, protocol.LogChunk{Data: []byte("no")}, map[string]string{
		"Authorization": "Bearer secret", "X-Machinist-Instance": "worker-log", "X-Machinist-Lease": "wrong",
	})
	if badLease.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(badLease.Body)
		t.Fatalf("bad lease status = %d: %s", badLease.StatusCode, body)
	}
	badLease.Body.Close()

	headers := map[string]string{
		"Authorization": "Bearer secret", "X-Machinist-Instance": "worker-log", "X-Machinist-Lease": "lease-log",
	}
	for index := 0; index < 17; index++ {
		response := postRunLog(t, web.URL, protocol.LogChunk{Offset: int64(index * maxRunLogChunkBytes), Data: bytes.Repeat([]byte{byte(index)}, maxRunLogChunkBytes)}, headers)
		if response.StatusCode != http.StatusNoContent {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			t.Fatalf("append %d status = %d: %s", index, response.StatusCode, body)
		}
		response.Body.Close()
	}
	duplicate := postRunLog(t, web.URL, protocol.LogChunk{Offset: 0, Data: []byte("duplicate")}, headers)
	if duplicate.StatusCode != http.StatusNoContent {
		t.Fatalf("duplicate status = %d", duplicate.StatusCode)
	}
	duplicate.Body.Close()

	getReadAPIBody(t, web.URL+"/api/v1/runs/run-log/log?offset=0", nil, http.StatusForbidden)
	body := getReadAPIBody(t, web.URL+"/api/v1/runs/run-log/log?offset=0", workerAuthorization(), http.StatusOK)
	var log runLogResponse
	if err := json.Unmarshal(body, &log); err != nil {
		t.Fatal(err)
	}
	if log.Offset != maxRunLogChunkBytes || log.NextOffset != 17*maxRunLogChunkBytes || len(log.Data) != maxRunLogTailBytes || !log.Truncated || log.Done {
		t.Fatalf("log response = offset %d, next %d, bytes %d, truncated %v, done %v", log.Offset, log.NextOffset, len(log.Data), log.Truncated, log.Done)
	}
	if log.Data[0] != 1 || log.Data[len(log.Data)-1] != 16 {
		t.Fatalf("tail bounds = %d..%d", log.Data[0], log.Data[len(log.Data)-1])
	}

	if _, err := server.store.db.Exec(`UPDATE runs SET state='succeeded' WHERE id='run-log'`); err != nil {
		t.Fatal(err)
	}
	body = getReadAPIBody(t, web.URL+"/api/v1/runs/run-log/log?offset="+strconv.FormatInt(log.NextOffset, 10), workerAuthorization(), http.StatusOK)
	if err := json.Unmarshal(body, &log); err != nil {
		t.Fatal(err)
	}
	if !log.Done || log.Data != "" || log.Offset != log.NextOffset {
		t.Fatalf("finished log = %#v", log)
	}
}

func postRunLog(t *testing.T, endpoint string, chunk protocol.LogChunk, headers map[string]string) *http.Response {
	t.Helper()
	body, err := json.Marshal(chunk)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, endpoint+"/api/v1/runs/run-log/log", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
