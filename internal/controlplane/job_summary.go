package controlplane

import "time"

// JobSummary is the prompt-free representation used by collection endpoints.
// Keep text that can contain task specifications out of this type.
type JobSummary struct {
	Task             *TaskSummary      `json:"task,omitempty"`
	Workflow         *WorkflowProgress `json:"workflow,omitempty"`
	AttentionReason  *string           `json:"attention_reason"`
	WaitingSince     *time.Time        `json:"waiting_since"`
	Labels           map[string]string `json:"labels"`
	Superseded       bool              `json:"superseded"`
	ID               string            `json:"id"`
	Repository       string            `json:"repository"`
	GitHubIssueTitle string            `json:"github_issue_title,omitempty"`
	Command          string            `json:"command"`
	TriggerID        string            `json:"trigger_id,omitempty"`
	OccurrenceKey    string            `json:"occurrence_key,omitempty"`
	TriggerSubject   string            `json:"trigger_subject,omitempty"`
	State            string            `json:"state"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	Runs             []Run             `json:"runs"`
}

type TaskSummary struct {
	Title     string `json:"title"`
	SourceURL string `json:"source_url"`
}

func newJobSummary(job Job) JobSummary {
	summary := JobSummary{
		Labels:           map[string]string{},
		Workflow:         job.Workflow,
		ID:               job.ID,
		Repository:       job.Repository,
		GitHubIssueTitle: job.GitHubIssueTitle,
		Command:          job.Command,
		TriggerID:        job.TriggerID,
		OccurrenceKey:    job.OccurrenceKey,
		TriggerSubject:   job.TriggerSubject,
		State:            job.State,
		CreatedAt:        job.CreatedAt,
		UpdatedAt:        job.UpdatedAt,
		Runs:             job.Runs,
	}
	if job.Task != nil {
		summary.Task = &TaskSummary{Title: job.Task.Title, SourceURL: job.Task.SourceURL}
	}
	if summary.Runs == nil {
		summary.Runs = []Run{}
	}
	applyJobAttention(&summary)
	return summary
}

func cappedJobSummaries(jobs []Job, finishedLimit int) []JobSummary {
	summaries := make([]JobSummary, 0, len(jobs))
	finished := 0
	for _, job := range jobs {
		if terminalJobState(job.State) {
			if finished >= finishedLimit {
				continue
			}
			finished++
		}
		summaries = append(summaries, newJobSummary(job))
	}
	return summaries
}
