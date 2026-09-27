package controlplane

import "net/http"

func (s *Server) registerFAC06Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/jobs", s.listJobs)
	mux.HandleFunc("GET /api/v1/jobs/{id}", s.authorizeArtifact(s.jobDetail))
	mux.HandleFunc("GET /api/v1/runs/{id}", s.authorizeArtifact(s.runDetail))
	mux.HandleFunc("GET /api/v1/events", s.listEvents)
	mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", s.authorizeSubmission(s.cancelJob))
	mux.HandleFunc("POST /api/v1/executors/{worker}/{executor}/clear-rate-limit", s.authorizeSubmission(s.clearRateLimit))
	mux.HandleFunc("POST /api/v1/runs/{id}/log", s.authorizeWorker(s.appendRunLog))
	mux.HandleFunc("GET /api/v1/runs/{id}/log", s.authorizeArtifact(s.runLog))
	mux.HandleFunc("GET /api/v1/usage", s.usageRollup)
	mux.HandleFunc("POST /api/v1/jobs/{id}/rewind", s.authorizeSubmission(s.rewindJob))
}

func notImplemented(response http.ResponseWriter) {
	http.Error(response, "not implemented", http.StatusNotImplemented)
}
