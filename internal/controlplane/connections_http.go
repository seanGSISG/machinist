package controlplane

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/owainlewis/machinist/internal/protocol"
)

// loginTokenHeader carries the token issued when a login starts. It binds
// every later read, input and cancel to the browser tab that started it.
const loginTokenHeader = "X-Machinist-Login-Token"

type connectionView struct {
	ExecutorAuthStatus
	Session *LoginSessionSummary `json:"session,omitempty"`
}

type connectionsResponse struct {
	Connections []connectionView `json:"connections"`
}

type loginStartRequest struct {
	Replace bool `json:"replace"`
}

type loginStartResponse struct {
	Session LoginSessionView `json:"session"`
	Token   string           `json:"token"`
}

type loginInputRequest struct {
	Text string `json:"text"`
	Key  string `json:"key"`
}

func (s *Server) connections(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	statuses, err := s.store.ExecutorAuthStatuses(request.Context(), s.store.now().UTC().Add(-workerAvailabilityWindow))
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	active := s.logins.active()
	result := connectionsResponse{Connections: make([]connectionView, 0, len(statuses))}
	for _, status := range statuses {
		view := connectionView{ExecutorAuthStatus: status}
		if session, ok := active[status.Worker+"\x00"+status.Executor]; ok {
			view.Session = &session
		}
		result.Connections = append(result.Connections, view)
	}
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) startLogin(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if !limitRequestBody(response, request, maxRequestBytes) {
		return
	}
	var input loginStartRequest
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	worker, executor := request.PathValue("worker"), request.PathValue("executor")
	statuses, err := s.store.ExecutorAuthStatuses(request.Context(), s.store.now().UTC().Add(-workerAvailabilityWindow))
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	var target *ExecutorAuthStatus
	for index := range statuses {
		if statuses[index].Worker == worker && statuses[index].Executor == executor {
			target = &statuses[index]
		}
	}
	switch {
	case target == nil || !target.Login:
		writeError(response, http.StatusNotFound, fmt.Errorf("worker %q has no login recipe for executor %q", worker, executor))
		return
	case !target.Online:
		writeError(response, http.StatusConflict, fmt.Errorf("worker %q is offline", worker))
		return
	}
	session, token, err := s.logins.start(worker, target.InstanceID, executor, target.LoginTimeout, input.Replace)
	if errors.Is(err, ErrLoginActive) {
		writeError(response, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusCreated, loginStartResponse{Session: session, Token: token})
}

func (s *Server) loginSession(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	session, err := s.logins.get(request.PathValue("id"), request.Header.Get(loginTokenHeader))
	if err != nil {
		writeError(response, http.StatusNotFound, err)
		return
	}
	writeJSON(response, http.StatusOK, session)
}

func (s *Server) loginInput(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if !limitRequestBody(response, request, 16<<10) {
		return
	}
	var input loginInputRequest
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	input.Key = strings.TrimSpace(input.Key)
	err := s.logins.input(request.PathValue("id"), request.Header.Get(loginTokenHeader), input.Text, input.Key)
	switch {
	case errors.Is(err, ErrLoginNotFound):
		writeError(response, http.StatusNotFound, err)
	case errors.Is(err, ErrLoginEnded):
		writeError(response, http.StatusConflict, err)
	case err != nil:
		writeError(response, http.StatusBadRequest, err)
	default:
		response.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) cancelLogin(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	session, err := s.logins.cancel(request.PathValue("id"), request.Header.Get(loginTokenHeader))
	switch {
	case errors.Is(err, ErrLoginNotFound):
		writeError(response, http.StatusNotFound, err)
	case errors.Is(err, ErrLoginEnded):
		writeError(response, http.StatusConflict, err)
	case err != nil:
		writeError(response, http.StatusInternalServerError, err)
	default:
		writeJSON(response, http.StatusOK, session)
	}
}

// authSync records a worker's auth states and exchanges login progress for
// queued login actions.
func (s *Server) authSync(response http.ResponseWriter, request *http.Request) {
	if !limitRequestBody(response, request, maxRequestBytes) {
		return
	}
	var input protocol.AuthSyncRequest
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	if strings.TrimSpace(input.InstanceID) == "" || strings.TrimSpace(input.Name) == "" {
		writeError(response, http.StatusBadRequest, errors.New("worker instance_id and name are required"))
		return
	}
	if err := s.store.RecordExecutorAuth(request.Context(), input); err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, protocol.AuthSyncResponse{Actions: s.logins.sync(input.InstanceID, input.Sessions, input.Active)})
}
