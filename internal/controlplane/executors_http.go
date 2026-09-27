package controlplane

import "net/http"

func (s *Server) clearRateLimit(response http.ResponseWriter, _ *http.Request) {
	notImplemented(response)
}
