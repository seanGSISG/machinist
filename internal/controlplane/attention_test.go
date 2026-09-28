package controlplane

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestAttentionDerived(t *testing.T) {
	server, web := newTestHTTPServer(t)
	defer web.Close()
	waiting := time.Date(2026, 9, 27, 10, 30, 0, 0, time.UTC)
	server.store.now = func() time.Time { return waiting.Add(time.Hour) }
	insertReadAPIJob(t, server, "job_gate", "awaiting_approval", "gate prompt")
	insertReadAPIJob(t, server, "job_blocked", "blocked", "blocked prompt")
	insertReadAPIJob(t, server, "job_running", "running", "running prompt")
	if _, err := server.store.db.Exec(`UPDATE jobs SET updated_at=?`, waiting.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.db.Exec(`INSERT INTO workers(instance_id,name,last_seen_at) VALUES('worker-instance','worker-one',?);
INSERT INTO worker_executor_auth(worker_instance,executor,login,login_timeout_ms,state,detail,updated_at) VALUES('worker-instance','codex',1,0,'expired','login expired',?)`, waiting.Format(time.RFC3339Nano), waiting.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	response, err := http.Get(web.URL + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var status StatusResponse
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	assertAttentionItem(t, status.GatesAwaitingApproval, "job_gate", "awaiting_approval", attentionGateBlocked, waiting, web.URL+"/#/gate/job_gate")
	assertAttentionItem(t, status.BlockedJobs, "job_blocked", "blocked", attentionBlocked, waiting, web.URL+"/#/runs/job_blocked")
	assertAttentionItem(t, status.Logins, "worker-one/codex", "expired", attentionLoginExpired, waiting, web.URL+"/#/connections")
	if len(status.Connections) != 1 || status.Connections[0].State != "expired" {
		t.Fatalf("connections changed = %#v", status.Connections)
	}

	summaries := make(map[string]JobSummary, len(status.Jobs))
	for _, job := range status.Jobs {
		summaries[job.ID] = job
	}
	for id, reason := range map[string]string{"job_gate": attentionGateBlocked, "job_blocked": attentionBlocked} {
		job := summaries[id]
		if job.AttentionReason == nil || *job.AttentionReason != reason || job.WaitingSince == nil || !job.WaitingSince.Equal(waiting) {
			t.Fatalf("summary %s attention = reason %v since %v", id, job.AttentionReason, job.WaitingSince)
		}
	}
	if job := summaries["job_running"]; job.AttentionReason != nil || job.WaitingSince != nil {
		t.Fatalf("running summary has attention: %#v", job)
	}

	var listed jobsResponse
	if err := json.Unmarshal(getReadAPIBody(t, web.URL+"/api/v1/jobs?state=blocked", nil, http.StatusOK), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Jobs) != 1 || listed.Jobs[0].AttentionReason == nil || *listed.Jobs[0].AttentionReason != attentionBlocked || listed.Jobs[0].WaitingSince == nil {
		t.Fatalf("jobs list attention = %#v", listed.Jobs)
	}
}

func assertAttentionItem(t *testing.T, items []StatusItem, id, state, reason string, since time.Time, htmlURL string) {
	t.Helper()
	if len(items) != 1 {
		t.Fatalf("%s items = %#v, want exactly one", reason, items)
	}
	item := items[0]
	if item.ID != id || item.State != state || item.Reason != reason || item.Message == "" || !item.Since.Equal(since) || item.HTMLURL != htmlURL {
		t.Fatalf("%s item = %#v", reason, item)
	}
}
