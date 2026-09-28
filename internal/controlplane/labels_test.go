package controlplane

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestSubmitLabels(t *testing.T) {
	server, web := newTestHTTPServer(t)
	defer web.Close()
	headers := submissionHeaders(t, web.URL)

	response := postJSON(t, web.URL+"/api/v1/jobs", map[string]any{
		"prompt": "work", "repository": "machinist", "command": "plan",
		"labels": map[string]string{"ticket": "20", "round": "2", "branch": "factory/t12"},
	}, headers)
	if response.StatusCode != http.StatusCreated {
		response.Body.Close()
		t.Fatalf("submit status = %d", response.StatusCode)
	}
	var created map[string]string
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	var count int
	if err := server.store.db.QueryRow(`SELECT COUNT(*) FROM job_labels WHERE job_id=?`, created["id"]).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("label count = %d, want 3", count)
	}
	jobsBody := getReadAPIBody(t, web.URL+"/api/v1/jobs?label=ticket:20", nil, http.StatusOK)
	var jobs jobsResponse
	if err := json.Unmarshal(jobsBody, &jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs.Jobs) != 1 || jobs.Jobs[0].Labels["branch"] != "factory/t12" || jobs.Jobs[0].Superseded {
		t.Fatalf("jobs = %#v", jobs.Jobs)
	}

	for _, labels := range []map[string]string{
		{"": "value"}, {"UPPER": "value"}, {"bad:key": "value"},
		{"a": string(make([]byte, 257))},
	} {
		invalid := postJSON(t, web.URL+"/api/v1/jobs", map[string]any{
			"prompt": "work", "repository": "machinist", "command": "plan", "labels": labels,
		}, headers)
		if invalid.StatusCode != http.StatusBadRequest {
			invalid.Body.Close()
			t.Fatalf("invalid labels %#v status = %d", labels, invalid.StatusCode)
		}
		invalid.Body.Close()
	}
}

func TestSupersedeAtomic(t *testing.T) {
	server, web := newTestHTTPServer(t)
	defer web.Close()
	headers := submissionHeaders(t, web.URL)
	oldID, err := server.store.CreateJob(t.Context(), "old", "machinist", "plan", testAgent("plan", "Plan"))
	if err != nil {
		t.Fatal(err)
	}

	response := postJSON(t, web.URL+"/api/v1/jobs", map[string]any{
		"prompt": "replacement", "repository": "machinist", "command": "plan",
		"labels": map[string]string{"ticket": "20"}, "supersedes_job_id": oldID,
	}, headers)
	if response.StatusCode != http.StatusCreated {
		response.Body.Close()
		t.Fatalf("supersede status = %d", response.StatusCode)
	}
	response.Body.Close()
	var oldState string
	var replacements int
	if err := server.store.db.QueryRow(`SELECT state FROM jobs WHERE id=?`, oldID).Scan(&oldState); err != nil {
		t.Fatal(err)
	}
	if err := server.store.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE supersedes_job_id=?`, oldID).Scan(&replacements); err != nil {
		t.Fatal(err)
	}
	if oldState != "cancelled" || replacements != 1 {
		t.Fatalf("old state = %q, replacements = %d", oldState, replacements)
	}

	var oldSummary JobSummary
	for _, summary := range getStatus(t, web.URL).Jobs {
		if summary.ID == oldID {
			oldSummary = summary
		}
	}
	if !oldSummary.Superseded {
		t.Fatalf("old summary = %#v", oldSummary)
	}

	finishedID, err := server.store.CreateJob(t.Context(), "finished", "machinist", "plan", testAgent("plan", "Plan"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.db.Exec(`UPDATE jobs SET state='succeeded' WHERE id=?`, finishedID); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := server.store.db.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	conflict := postJSON(t, web.URL+"/api/v1/jobs", map[string]any{
		"prompt": "must roll back", "repository": "machinist", "command": "plan", "supersedes_job_id": finishedID,
	}, headers)
	if conflict.StatusCode != http.StatusConflict {
		conflict.Body.Close()
		t.Fatalf("finished supersede status = %d", conflict.StatusCode)
	}
	conflict.Body.Close()
	var after int
	if err := server.store.db.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("job count after conflict = %d, want %d", after, before)
	}
}

func submissionHeaders(t *testing.T, endpoint string) map[string]string {
	t.Helper()
	poll := postJSON(t, endpoint+"/api/v1/workers/poll", map[string]any{
		"instance_id": "labels-worker", "name": "labels-worker", "executors": []string{"test"}, "repositories": []string{"machinist"},
	}, map[string]string{"Authorization": "Bearer secret"})
	if poll.StatusCode != http.StatusOK {
		poll.Body.Close()
		t.Fatalf("worker poll status = %d", poll.StatusCode)
	}
	poll.Body.Close()
	status := getStatus(t, endpoint)
	return map[string]string{"Origin": endpoint, "X-Machinist-CSRF": status.CSRFToken}
}
