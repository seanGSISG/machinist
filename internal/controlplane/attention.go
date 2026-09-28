package controlplane

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	attentionGateBlocked  = "GateAwaitingApproval"
	attentionBlocked      = "Blocked"
	attentionLoginExpired = "LoginExpired"
	attentionLoginRecheck = "LoginRechecking"
)

func deriveAttention(request *http.Request, jobs []JobSummary, connections []ExecutorAuthStatus) (gates, blocked, logins []StatusItem) {
	gates = []StatusItem{}
	blocked = []StatusItem{}
	logins = []StatusItem{}
	base := requestBaseURL(request)
	for index := range jobs {
		job := &jobs[index]
		applyJobAttention(job)
		var reason, message, path string
		switch job.State {
		case "awaiting_approval":
			reason = attentionGateBlocked
			message = "Approval is required before this task can continue."
			path = "/#/gate/" + url.PathEscape(job.ID)
		case "blocked":
			reason = attentionBlocked
			message = "This task is blocked and needs human input."
			path = "/#/runs/" + url.PathEscape(job.ID)
		default:
			continue
		}
		item := StatusItem{ID: job.ID, State: job.State, Since: job.UpdatedAt, Reason: reason, Message: message, HTMLURL: base + path}
		if job.State == "awaiting_approval" {
			gates = append(gates, item)
		} else {
			blocked = append(blocked, item)
		}
	}
	for _, connection := range connections {
		var reason, message string
		switch connection.State {
		case authStateExpired:
			reason = attentionLoginExpired
			message = connection.Executor + " login on " + connection.Worker + " has expired."
		case authStateRechecking:
			reason = attentionLoginRecheck
			message = connection.Executor + " login on " + connection.Worker + " is being rechecked."
		default:
			continue
		}
		logins = append(logins, StatusItem{
			ID: connection.Worker + "/" + connection.Executor, State: connection.State, Since: connection.UpdatedAt,
			Reason: reason, Message: message, HTMLURL: base + "/#/connections",
		})
	}
	return gates, blocked, logins
}

func applyJobAttention(job *JobSummary) {
	var reason string
	switch job.State {
	case "awaiting_approval":
		reason = attentionGateBlocked
	case "blocked":
		reason = attentionBlocked
	default:
		job.AttentionReason = nil
		job.WaitingSince = nil
		return
	}
	job.AttentionReason = stringPointer(reason)
	job.WaitingSince = timePointer(job.UpdatedAt)
}

func requestBaseURL(request *http.Request) string {
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + strings.TrimSuffix(request.Host, "/")
}

func stringPointer(value string) *string { return &value }

func timePointer(value time.Time) *time.Time { return &value }
