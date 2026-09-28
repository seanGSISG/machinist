package controlplane

import (
	"errors"
	"net/http"
	"time"
)

type usageRollupRow struct {
	Key               string `json:"key"`
	Runs              int64  `json:"runs"`
	InputTokens       int64  `json:"input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	CachedInputTokens int64  `json:"cached_input_tokens"`
	ReasoningTokens   int64  `json:"reasoning_tokens"`
}

type usageRollupResponse struct {
	GroupBy string           `json:"group_by"`
	Since   string           `json:"since"`
	Rows    []usageRollupRow `json:"rows"`
}

func (s *Server) usageRollup(response http.ResponseWriter, request *http.Request) {
	groupBy := request.URL.Query().Get("group_by")
	dimension, ok := map[string]string{
		"executor": "runs.executor",
		"model":    "run_usage.model",
		"command":  "runs.command",
		"ticket":   "COALESCE(job_labels.value, '')",
	}[groupBy]
	if !ok {
		writeError(response, http.StatusBadRequest, errors.New("group_by must be executor, model, command, or ticket"))
		return
	}

	since := ""
	if raw := request.URL.Query().Get("since"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(response, http.StatusBadRequest, errors.New("since must be RFC3339"))
			return
		}
		since = parsed.UTC().Format(time.RFC3339Nano)
	}

	query := `SELECT ` + dimension + ` AS rollup_key,
COUNT(*), SUM(run_usage.input_tokens), SUM(run_usage.output_tokens),
SUM(run_usage.cached_input_tokens), SUM(run_usage.reasoning_tokens)
FROM run_usage
JOIN runs ON runs.id=run_usage.run_id
JOIN jobs ON jobs.id=runs.job_id
LEFT JOIN job_labels ON job_labels.job_id=jobs.id AND job_labels.label_key='ticket'
WHERE (?='' OR run_usage.created_at>=?)
GROUP BY rollup_key
ORDER BY SUM(run_usage.input_tokens) DESC, rollup_key ASC`
	rows, err := s.store.db.QueryContext(request.Context(), query, since, since)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	result := usageRollupResponse{GroupBy: groupBy, Since: since, Rows: []usageRollupRow{}}
	for rows.Next() {
		var row usageRollupRow
		if err := rows.Scan(&row.Key, &row.Runs, &row.InputTokens, &row.OutputTokens, &row.CachedInputTokens, &row.ReasoningTokens); err != nil {
			writeError(response, http.StatusInternalServerError, err)
			return
		}
		result.Rows = append(result.Rows, row)
	}
	if err := rows.Err(); err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}
