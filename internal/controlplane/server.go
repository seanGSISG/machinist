package controlplane

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

// The runner records up to 64 MiB before base64 encoding. The JSON envelope remains
// below this limit, including per-event and string-escaping overhead.
const maxCompletionBytes = 96 << 20

const maxRequestBytes = 1 << 20

const workerAvailabilityWindow = 15 * time.Second

// The bundle is built, not committed (`just frontend`); .gitkeep lets a Go-only build compile.
//
//go:embed all:web/dist
var webAssets embed.FS

type Server struct {
	store             *Store
	definitionPath    string
	configMu          sync.Mutex
	lastGoodConfig    config.Config
	invalidCommands   []config.InvalidCommand
	triggers          []config.ResolvedTrigger
	github            githubTriggerClient
	schedulerEvery    time.Duration
	now               func() time.Time
	schedulerError    func(error)
	shutdownTimeout   time.Duration
	maxConcurrentJobs int
	workerToken       string
	csrfToken         string
	trustedOrigins    map[string]bool
	logins            *loginBroker
	handler           http.Handler
}

type submitRequest struct {
	Labels          map[string]string `json:"labels,omitempty"`
	SupersedesJobID string            `json:"supersedes_job_id,omitempty"`
	Title           string            `json:"title"`
	SourceURL       string            `json:"source_url"`
	Spec            string            `json:"spec"`
	Workflow        string            `json:"workflow"`
	Prompt          string            `json:"prompt"`
	Repository      string            `json:"repository"`
	Command         string            `json:"command"`
	Model           string            `json:"model"`
}

type commandDefinitionResponse struct {
	Name     string `json:"name"`
	Executor string `json:"executor"`
	Model    string `json:"model,omitempty"`
	Timeout  string `json:"timeout"`
	Hash     string `json:"hash"`
	Prompt   string `json:"prompt"`
}

type workflowStepDefinition struct {
	Name     string `json:"name"`
	Approval bool   `json:"approval"`
}

type definitionsResponse struct {
	Workflows map[string][]workflowStepDefinition `json:"workflows"`
	Commands  []commandDefinitionResponse         `json:"commands"`
}

type catalogResponse struct {
	Workflows    []string `json:"workflows"`
	Commands     []string `json:"commands"`
	Repositories []string `json:"repositories"`
}

func NewServer(store *Store, definitionPath, workerToken string, maxConcurrentJobs int) (*Server, error) {
	if maxConcurrentJobs < 0 {
		return nil, errors.New("max concurrent jobs cannot be negative")
	}
	csrfToken, err := randomID("csrf", 24)
	if err != nil {
		return nil, err
	}
	report, err := config.ValidateFile(definitionPath)
	if err != nil {
		return nil, err
	}
	definition := report.Config
	managedTriggers, e := definition.ResolveTriggers()
	if e != nil {
		return nil, e
	}
	storage, e := definition.ResolveStorage(store.storageConfig.Path)
	if e != nil {
		return nil, e
	}
	if e = store.configureStorage(storage); e != nil {
		return nil, e
	}
	startup := time.Now().UTC()
	definitions := make([]TriggerDefinition, 0, len(managedTriggers))
	for _, trigger := range managedTriggers {
		definitions = append(definitions, TriggerDefinition{
			Identity: trigger.Identity, Family: trigger.Family,
			ConfigSignature: trigger.Signature, NextDueAt: trigger.FirstDue(startup),
		})
	}
	if err := store.SyncTriggers(context.Background(), definitions); err != nil {
		return nil, fmt.Errorf("restore managed triggers: %w", err)
	}
	server := &Server{
		store: store, definitionPath: definitionPath, lastGoodConfig: definition,
		invalidCommands: append([]config.InvalidCommand(nil), report.InvalidCommands...), triggers: managedTriggers,
		github: NewGitHubCLI("gh", 30*time.Second), now: time.Now,
		schedulerEvery: 30 * time.Second, shutdownTimeout: 5 * time.Second,
		schedulerError:    func(err error) { log.Printf("scheduler: %v", err) },
		maxConcurrentJobs: maxConcurrentJobs, workerToken: workerToken, csrfToken: csrfToken,
	}
	server.logins = newLoginBroker(func() time.Time { return server.store.now() })
	server.handler, err = server.routes()
	if err != nil {
		return nil, err
	}
	return server, nil
}

func (s *Server) Handler() http.Handler { return s.handler }

// loadDefinitionFile applies syntactically valid reloads and retains the last
// good file when a write is incomplete or otherwise not valid TOML.
func (s *Server) loadDefinitionFile() (config.Config, []config.InvalidCommand, error) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	report, err := config.ValidateFile(s.definitionPath)
	if err != nil {
		if s.lastGoodConfig.Path() != "" {
			return s.lastGoodConfig, append([]config.InvalidCommand(nil), s.invalidCommands...), nil
		}
		return config.Config{}, nil, err
	}
	s.lastGoodConfig = report.Config
	s.invalidCommands = append([]config.InvalidCommand(nil), report.InvalidCommands...)
	return s.lastGoodConfig, append([]config.InvalidCommand(nil), s.invalidCommands...), nil
}

func (s *Server) currentInvalidCommands() []config.InvalidCommand {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	return append([]config.InvalidCommand(nil), s.invalidCommands...)
}

func (s *Server) Serve(ctx context.Context, listen string, onListening func(net.Addr)) error {
	if err := validateLoopbackListen(listen); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listen, err)
	}
	defer listener.Close()
	if onListening != nil {
		onListening(listener.Addr())
	}
	httpServer := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	done := make(chan error, 1)
	go func() {
		err := httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()
	schedulerCtx, stopScheduler := context.WithCancel(ctx)
	defer stopScheduler()
	schedulerDone := make(chan error, 1)
	go func() { schedulerDone <- s.runScheduler(schedulerCtx) }()
	stopHTTP := func() error {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
		defer cancel()
		shutdownErr := httpServer.Shutdown(shutdownCtx)
		var forceCloseErr error
		if shutdownErr != nil {
			forceCloseErr = httpServer.Close()
		}
		// Shutdown can run before Serve registers the listener when the
		// callback cancels the context. Close it explicitly and wait for the
		// serving goroutine so every cancellation path releases the socket.
		closeErr := listener.Close()
		<-done
		var shutdownFailure error
		if shutdownErr != nil {
			shutdownFailure = fmt.Errorf("stop control plane: %w", shutdownErr)
		}
		if forceCloseErr != nil && !errors.Is(forceCloseErr, http.ErrServerClosed) {
			shutdownFailure = errors.Join(shutdownFailure, fmt.Errorf("force close control plane: %w", forceCloseErr))
		}
		if closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			shutdownFailure = errors.Join(shutdownFailure, fmt.Errorf("stop control plane listener: %w", closeErr))
		}
		return shutdownFailure
	}
	select {
	case err := <-done:
		stopScheduler()
		return errors.Join(err, <-schedulerDone)
	case err := <-schedulerDone:
		if shutdownErr := stopHTTP(); shutdownErr != nil && err == nil {
			return shutdownErr
		}
		return err
	case <-ctx.Done():
		stopScheduler()
		shutdownErr := stopHTTP()
		return errors.Join(shutdownErr, <-schedulerDone)
	}
}

func (s *Server) runScheduler(ctx context.Context) error {
	var schedulers sync.WaitGroup
	loop := func(delayFirst bool, work func(context.Context) error) {
		schedulers.Add(1)
		go func() {
			defer schedulers.Done()
			if delayFirst && !sleep(ctx, s.schedulerEvery) {
				return
			}
			for {
				s.reportSchedulerError(work(ctx))
				if !sleep(ctx, s.schedulerEvery) {
					return
				}
			}
		}()
	}
	for _, trigger := range s.triggers {
		loop(false, func(ctx context.Context) error {
			if err := s.processManagedTrigger(ctx, trigger); err != nil {
				return fmt.Errorf("trigger %q: %w", trigger.Identity, err)
			}
			return nil
		})
	}
	loop(true, s.maintainState)
	loop(true, func(ctx context.Context) error { return s.pruneRetention(ctx, s.now().UTC()) })
	loop(true, s.store.CleanupArtifacts)
	<-ctx.Done()
	schedulers.Wait()
	return nil
}

// sleep waits for the duration and reports false when ctx ends first.
func sleep(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Server) maintainState(ctx context.Context) error {
	_, reclaimErr := s.store.ReclaimExpiredLeases(ctx)
	_, pruneErr := s.store.PruneSupersededWorkers(ctx, s.store.now().UTC().Add(-workerAvailabilityWindow))
	return errors.Join(reclaimErr, pruneErr)
}

func (s *Server) reportSchedulerError(err error) {
	if err != nil && !errors.Is(err, context.Canceled) && s.schedulerError != nil {
		s.schedulerError(err)
	}
}

func (s *Server) routes() (http.Handler, error) {
	dist, err := fs.Sub(webAssets, "web/dist")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/v1/runs/{id}/artifacts", s.authorizeWorker(s.uploadArtifact))
	mux.HandleFunc("GET /api/v1/jobs/{id}/artifacts", s.authorizeArtifact(s.listArtifacts))
	mux.HandleFunc("GET /api/v1/artifacts/{id}/content", s.authorizeArtifact(s.artifactContent))
	mux.HandleFunc("GET /api/v1/status", s.status)
	mux.HandleFunc("GET /api/v1/catalog", s.catalog)
	mux.HandleFunc("GET /api/v1/definitions", s.definitions)
	mux.HandleFunc("POST /api/v1/jobs/{id}/{action}", s.authorizeSubmission(s.workflowAction))
	mux.HandleFunc("POST /api/v1/jobs", s.authorizeSubmission(s.submit))
	mux.HandleFunc("DELETE /api/v1/jobs/{id}", s.authorizeSubmission(s.deleteJob))
	mux.HandleFunc("GET /api/v1/settings", s.settings)
	mux.HandleFunc("GET /api/v1/settings/{kind}/{name}/history", s.settingHistory)
	mux.HandleFunc("PUT /api/v1/settings/{kind}/{name}", s.authorizeSubmission(s.putSetting))
	mux.HandleFunc("POST /api/v1/settings/versions/{id}/revert", s.authorizeSubmission(s.revertSetting))
	mux.HandleFunc("GET /api/v1/connections", s.connections)
	mux.HandleFunc("POST /api/v1/connections/{worker}/{executor}/login", s.authorizeSubmission(s.startLogin))
	mux.HandleFunc("GET /api/v1/connections/sessions/{id}", s.loginSession)
	mux.HandleFunc("POST /api/v1/connections/sessions/{id}/input", s.authorizeSubmission(s.loginInput))
	mux.HandleFunc("POST /api/v1/connections/sessions/{id}/cancel", s.authorizeSubmission(s.cancelLogin))
	mux.HandleFunc("POST /api/v1/workers/auth", s.authorizeWorker(s.authSync))
	mux.HandleFunc("POST /api/v1/workers/poll", s.authorizeWorker(s.poll))
	mux.HandleFunc("POST /api/v1/runs/{id}/heartbeat", s.authorizeWorker(s.heartbeat))
	mux.HandleFunc("POST /api/v1/runs/{id}/complete", s.authorizeWorker(s.complete))
	s.registerFAC06Routes(mux)
	if _, err := fs.Stat(dist, "index.html"); err != nil {
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "web UI not built: run `just frontend`, then rebuild machinist", http.StatusServiceUnavailable)
		})
	} else {
		mux.Handle("/", http.FileServer(http.FS(dist)))
	}
	return securityHeaders(mux), nil
}

func (s *Server) definitions(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	definition, _, err := s.loadDefinitions(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	commands := make([]commandDefinitionResponse, 0, len(definition.Commands))
	for _, name := range definition.CommandNames() {
		command, err := definition.ResolveCommand(name)
		if err != nil {
			writeError(response, http.StatusInternalServerError, err)
			return
		}
		commands = append(commands, commandDefinitionResponse{Name: command.Name, Executor: command.Executor, Model: definition.DefaultModel(name), Timeout: command.Timeout.String(), Hash: command.Hash, Prompt: command.Prompt})
	}
	workflows := map[string][]workflowStepDefinition{}
	for _, name := range definition.WorkflowNames() {
		steps, err := definition.ResolveTaskWorkflow(name, "")
		if err != nil {
			writeError(response, http.StatusInternalServerError, err)
			return
		}
		for _, step := range steps {
			workflows[name] = append(workflows[name], workflowStepDefinition{Name: step.Command.Name, Approval: step.Approval})
		}
	}
	writeJSON(response, http.StatusOK, definitionsResponse{Commands: commands, Workflows: workflows})
}

func (s *Server) status(response http.ResponseWriter, request *http.Request) {
	snapshot, err := s.store.Snapshot(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	definition, _, err := s.loadDefinitions(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	now := s.store.now().UTC()
	for index := range snapshot.Workers {
		snapshot.Workers[index].Connected = !snapshot.Workers[index].LastSeenAt.Before(now.Add(-workerAvailabilityWindow))
	}
	repositories, repositoryErr := s.store.AvailableRepositories(request.Context(), now.Add(-workerAvailabilityWindow))
	if repositoryErr != nil {
		writeError(response, http.StatusInternalServerError, repositoryErr)
		return
	}
	connections, err := s.store.ExecutorAuthStatuses(request.Context(), now.Add(-workerAvailabilityWindow))
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	jobs := cappedJobSummaries(snapshot.Jobs, definition.Server.FinishedJobLimit())
	if err := s.store.enrichJobSummaries(request.Context(), jobs); err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	gates, blocked, logins := deriveAttention(request, jobs, connections)
	status := StatusResponse{
		SchemaVersion:         statusSchemaVersion,
		GeneratedAt:           now,
		Jobs:                  jobs,
		Workers:               snapshot.Workers,
		Triggers:              snapshot.Triggers,
		Connections:           connections,
		Commands:              definition.CommandNames(),
		Workflows:             definition.WorkflowNames(),
		Repositories:          repositories,
		Executors:             []ExecutorStatus{},
		GatesAwaitingApproval: gates,
		BlockedJobs:           blocked,
		Logins:                logins,
		CSRFToken:             s.csrfToken,
	}
	writeJSON(response, http.StatusOK, struct {
		StatusResponse
		InvalidCommands []config.InvalidCommand `json:"invalid_commands"`
	}{
		StatusResponse:  status,
		InvalidCommands: s.currentInvalidCommands(),
	})
}

func (s *Server) catalog(response http.ResponseWriter, request *http.Request) {
	definition, _, err := s.loadDefinitions(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	repositories, repositoryErr := s.store.KnownRepositories(request.Context())
	if repositoryErr != nil {
		writeError(response, http.StatusInternalServerError, repositoryErr)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, catalogResponse{
		Commands:     definition.CommandNames(),
		Workflows:    definition.WorkflowNames(),
		Repositories: repositories,
	})
}

func (s *Server) submit(response http.ResponseWriter, request *http.Request) {
	if !limitRequestBody(response, request, maxRequestBytes) {
		return
	}
	var input submitRequest
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	if err := validateLabels(input.Labels); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(input.Repository) == "" {
		writeError(response, http.StatusBadRequest, errors.New("repository is required"))
		return
	}
	repositories, repositoryErr := s.store.KnownRepositories(request.Context())
	if repositoryErr != nil {
		writeError(response, http.StatusInternalServerError, repositoryErr)
		return
	}
	if !slices.Contains(repositories, input.Repository) {
		writeError(response, http.StatusBadRequest, fmt.Errorf("repository %q is not defined in the control plane", input.Repository))
		return
	}
	input.Model = strings.TrimSpace(input.Model)
	if len(input.Model) > 128 || strings.ContainsAny(input.Model, "\x00\r\n") {
		writeError(response, http.StatusBadRequest, errors.New("model must be at most 128 characters on one line"))
		return
	}
	if input.Workflow != "" {
		if input.Command != "" {
			writeError(response, http.StatusBadRequest, errors.New("choose either workflow or command"))
			return
		}
		definition, _, err := s.loadDefinitions(request.Context())
		if err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		if input.Prompt != "" && (input.SourceURL != "" || input.Spec != "" || input.Title != "") {
			writeError(response, http.StatusBadRequest, errors.New("use spec instead of prompt for a task"))
			return
		}
		task := protocol.Task{Title: input.Title, SourceURL: input.SourceURL, Spec: input.Spec}
		// Normalize older clients at the boundary; every new workflow is a task.
		if input.Prompt != "" {
			task.Spec = input.Prompt
		}
		if err := task.Validate(); err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		steps, err := definition.ResolveTaskWorkflow(input.Workflow, input.Model)
		if err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		id, status, err := s.store.createLabeledTaskJob(request.Context(), task, input.Repository, input.Workflow, steps, input.Labels, input.SupersedesJobID)
		if err != nil {
			writeError(response, http.StatusInternalServerError, err)
			return
		}
		if status == http.StatusNotFound {
			writeError(response, status, errors.New("superseded job not found"))
			return
		}
		if status == http.StatusConflict {
			writeError(response, status, errors.New("superseded job is already finished"))
			return
		}
		writeJSON(response, http.StatusCreated, map[string]string{"id": id})
		return
	}
	if input.Title != "" || input.SourceURL != "" || input.Spec != "" {
		writeError(response, 400, errors.New("task fields require a workflow"))
		return
	}
	if strings.TrimSpace(input.Command) == "" {
		writeError(response, http.StatusBadRequest, errors.New("command is required"))
		return
	}
	definition, _, err := s.loadDefinitions(request.Context())
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	command, err := definition.ResolveCommand(input.Command)
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	command, err = config.RenderPrompt(command, input.Prompt)
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	command.Model = input.Model
	if command.Model == "" {
		command.Model = definition.DefaultModel(input.Command)
	}
	jobID, status, err := s.store.createLabeledJob(request.Context(), input.Prompt, input.Repository, input.Command, command, input.Labels, input.SupersedesJobID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	if status == http.StatusNotFound {
		writeError(response, status, errors.New("superseded job not found"))
		return
	}
	if status == http.StatusConflict {
		writeError(response, status, errors.New("superseded job is already finished"))
		return
	}
	writeJSON(response, http.StatusCreated, map[string]string{"id": jobID})
}

func (s *Server) deleteJob(response http.ResponseWriter, request *http.Request) {
	err := s.store.DeleteJob(request.Context(), request.PathValue("id"))
	if errors.Is(err, ErrJobActive) {
		writeError(response, http.StatusConflict, err)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(response, http.StatusNotFound, errors.New("job not found"))
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) poll(response http.ResponseWriter, request *http.Request) {
	if !limitRequestBody(response, request, maxRequestBytes) {
		return
	}
	var input protocol.PollRequest
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	if strings.TrimSpace(input.InstanceID) == "" || strings.TrimSpace(input.Name) == "" {
		writeError(response, http.StatusBadRequest, errors.New("worker instance_id and name are required"))
		return
	}
	run, err := s.store.poll(request.Context(), input, s.maxConcurrentJobs)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, protocol.PollResponse{Run: run})
}

func (s *Server) complete(response http.ResponseWriter, request *http.Request) {
	if !limitRequestBody(response, request, maxCompletionBytes) {
		return
	}
	var input protocol.Completion
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	if input.InstanceID == "" || input.LeaseToken == "" {
		writeError(response, http.StatusBadRequest, errors.New("instance_id and lease_token are required"))
		return
	}
	err := s.store.Complete(request.Context(), request.PathValue("id"), input)
	if errors.Is(err, ErrLeaseConflict) || errors.Is(err, ErrRunState) {
		writeError(response, http.StatusConflict, err)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(response, http.StatusNotFound, errors.New("run not found"))
		return
	}
	if errors.Is(err, ErrInvalidCompletion) {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		log.Printf("complete run %q: %v", request.PathValue("id"), err)
		writeError(response, http.StatusInternalServerError, errors.New("complete run"))
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) heartbeat(response http.ResponseWriter, request *http.Request) {
	if !limitRequestBody(response, request, maxRequestBytes) {
		return
	}
	var input protocol.Heartbeat
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	if input.InstanceID == "" || input.LeaseToken == "" {
		writeError(response, http.StatusBadRequest, errors.New("instance_id and lease_token are required"))
		return
	}
	err := s.store.Heartbeat(request.Context(), request.PathValue("id"), input)
	if errors.Is(err, ErrLeaseConflict) || errors.Is(err, ErrRunState) {
		writeError(response, http.StatusConflict, err)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(response, http.StatusNotFound, errors.New("run not found"))
		return
	}
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) authorizeWorker(next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if !s.validBearerRequest(request) {
			writeUnauthorized(response)
			return
		}
		next(response, request)
	}
}

func (s *Server) authorizeSubmission(next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "" {
			if !s.validBearerRequest(request) {
				writeUnauthorized(response)
				return
			}
		} else if !s.validBrowserRequest(request) {
			writeError(response, http.StatusForbidden, errors.New("invalid submission origin or CSRF token"))
			return
		}
		next(response, request)
	}
}

func (s *Server) validBearerRequest(request *http.Request) bool {
	provided := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	return provided != request.Header.Get("Authorization") && subtle.ConstantTimeCompare([]byte(provided), []byte(s.workerToken)) == 1
}

func writeUnauthorized(response http.ResponseWriter) {
	response.Header().Set("WWW-Authenticate", "Bearer")
	writeError(response, http.StatusUnauthorized, errors.New("invalid worker token"))
}

func (s *Server) validBrowserRequest(request *http.Request) bool {
	if subtle.ConstantTimeCompare([]byte(request.Header.Get("X-Machinist-CSRF")), []byte(s.csrfToken)) != 1 {
		return false
	}
	origin, err := url.Parse(request.Header.Get("Origin"))
	if err != nil {
		return false
	}
	// A configured origin is enough on its own: browsers set Origin and other sites cannot forge it,
	// and a proxy may rewrite Host on the way to the loopback listener.
	if s.trustedOrigins[strings.ToLower(origin.Scheme+"://"+origin.Host)] && origin.Host != "" {
		return true
	}
	if origin.Scheme != "http" || !strings.EqualFold(origin.Host, request.Host) {
		return false
	}
	hostname := origin.Hostname()
	return hostname == "localhost" || net.ParseIP(hostname) != nil && net.ParseIP(hostname).IsLoopback()
}

// TrustOrigins allows browser requests from these normalized origins (config.NormalizeOrigin), for a
// reverse proxy in front of the loopback listener that authenticates users itself.
func (s *Server) TrustOrigins(origins []string) {
	s.trustedOrigins = make(map[string]bool, len(origins))
	for _, origin := range origins {
		s.trustedOrigins[origin] = true
	}
}

func validateLoopbackListen(listen string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", listen, err)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen address %q must use a loopback host", listen)
	}
	return nil
}

func decodeJSON(request *http.Request, target any) error {
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("decode trailing JSON: %w", err)
		}
		return errors.New("request contains multiple JSON values")
	}
	return nil
}

func limitRequestBody(response http.ResponseWriter, request *http.Request, limit int64) bool {
	if request.ContentLength > limit {
		writeError(response, http.StatusRequestEntityTooLarge, errors.New("request body is too large"))
		return false
	}
	request.Body = http.MaxBytesReader(response, request.Body, limit)
	return true
}

func writeJSON(response http.ResponseWriter, status int, body any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(body)
}

func writeError(response http.ResponseWriter, status int, err error) {
	writeJSON(response, status, map[string]string{"error": err.Error()})
}

func writeDecodeError(response http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		writeError(response, http.StatusRequestEntityTooLarge, errors.New("request body is too large"))
		return
	}
	writeError(response, http.StatusBadRequest, err)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' blob:; media-src 'self' blob:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(response, request)
	})
}

func (s *Server) workflowAction(response http.ResponseWriter, request *http.Request) {
	if !limitRequestBody(response, request, maxRequestBytes) {
		return
	}
	var input struct {
		Feedback               string `json:"feedback"`
		RunID                  string `json:"run_id"`
		PreviousProcessStopped bool   `json:"previous_process_stopped"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	err := s.store.WorkflowAction(request.Context(), request.PathValue("id"), input.RunID, request.PathValue("action"), input.PreviousProcessStopped, input.Feedback)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(response, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, ErrWorkflowAction) {
		writeError(response, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"status": "accepted"})
}
