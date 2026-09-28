package controlplane

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

const (
	maxRunLogChunkBytes = 16 << 10
	maxRunLogTailBytes  = 256 << 10
)

type runLogResponse struct {
	Offset     int64  `json:"offset"`
	NextOffset int64  `json:"next_offset"`
	Data       string `json:"data"`
	Truncated  bool   `json:"truncated"`
	Done       bool   `json:"done"`
}

func (s *Server) appendRunLog(response http.ResponseWriter, request *http.Request) {
	if !limitRequestBody(response, request, maxRequestBytes) {
		return
	}
	var input protocol.LogChunk
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	if input.Offset < 0 || len(input.Data) > maxRunLogChunkBytes {
		writeError(response, http.StatusBadRequest, errors.New("log offset must be non-negative and data must not exceed 16 KiB"))
		return
	}
	instanceID := request.Header.Get("X-Machinist-Instance")
	leaseToken := request.Header.Get("X-Machinist-Lease")
	if instanceID == "" || leaseToken == "" {
		writeError(response, http.StatusBadRequest, errors.New("worker instance and lease token are required"))
		return
	}

	tx, err := s.store.db.BeginTx(request.Context(), nil)
	if err != nil {
		writeError(response, http.StatusInternalServerError, errors.New("append run log"))
		return
	}
	defer tx.Rollback()
	if err := s.checkRunLogLease(request, tx, instanceID, leaseToken); err != nil {
		writeRunLogError(response, err)
		return
	}

	var next int64
	var data []byte
	var truncated bool
	err = tx.QueryRowContext(request.Context(), `SELECT next_offset,data,truncated FROM run_log_tail WHERE run_id=?`, request.PathValue("id")).Scan(&next, &data, &truncated)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(response, http.StatusInternalServerError, errors.New("append run log"))
		return
	}
	if input.Offset < next {
		if err := tx.Commit(); err != nil {
			writeError(response, http.StatusInternalServerError, errors.New("append run log"))
			return
		}
		response.WriteHeader(http.StatusNoContent)
		return
	}
	if input.Offset > next {
		data = nil
		truncated = true
	}
	data = append(data, input.Data...)
	next = input.Offset + int64(len(input.Data))
	truncated = truncated || input.Truncated
	if len(data) > maxRunLogTailBytes {
		data = append([]byte(nil), data[len(data)-maxRunLogTailBytes:]...)
		truncated = true
	}
	_, err = tx.ExecContext(request.Context(), `INSERT INTO run_log_tail(run_id,next_offset,data,truncated,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(run_id) DO UPDATE SET next_offset=excluded.next_offset,data=excluded.data,truncated=excluded.truncated,updated_at=excluded.updated_at`, request.PathValue("id"), next, data, truncated, s.store.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		writeError(response, http.StatusInternalServerError, errors.New("append run log"))
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(response, http.StatusInternalServerError, errors.New("append run log"))
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) checkRunLogLease(request *http.Request, tx *sql.Tx, instanceID, leaseToken string) error {
	var state, storedInstance, storedLease string
	var expires sql.NullInt64
	err := tx.QueryRowContext(request.Context(), `SELECT state,COALESCE(worker_instance,''),COALESCE(lease_token,''),lease_expires_at FROM runs WHERE id=?`, request.PathValue("id")).Scan(&state, &storedInstance, &storedLease, &expires)
	if err != nil {
		return err
	}
	if state != "running" {
		return ErrRunState
	}
	if subtle.ConstantTimeCompare([]byte(storedInstance), []byte(instanceID)) != 1 || subtle.ConstantTimeCompare([]byte(storedLease), []byte(leaseToken)) != 1 || !expires.Valid || expires.Int64 <= s.store.now().UTC().UnixNano() {
		return ErrLeaseConflict
	}
	return nil
}

func writeRunLogError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(response, http.StatusNotFound, errors.New("run not found"))
	case errors.Is(err, ErrLeaseConflict), errors.Is(err, ErrRunState):
		writeError(response, http.StatusConflict, err)
	default:
		writeError(response, http.StatusInternalServerError, errors.New("run log"))
	}
}

func (s *Server) runLog(response http.ResponseWriter, request *http.Request) {
	offset := int64(0)
	if raw := request.URL.Query().Get("offset"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeError(response, http.StatusBadRequest, errors.New("offset must be a non-negative integer"))
			return
		}
		offset = parsed
	}
	var state string
	if err := s.store.db.QueryRowContext(request.Context(), `SELECT state FROM runs WHERE id=?`, request.PathValue("id")).Scan(&state); err != nil {
		writeRunLogError(response, err)
		return
	}
	var next int64
	var data []byte
	var storedTruncated bool
	err := s.store.db.QueryRowContext(request.Context(), `SELECT next_offset,data,truncated FROM run_log_tail WHERE run_id=?`, request.PathValue("id")).Scan(&next, &data, &storedTruncated)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeRunLogError(response, err)
		return
	}
	start := next - int64(len(data))
	actual := offset
	truncated := false
	if actual < start {
		actual = start
		truncated = true
	} else if actual > next {
		actual = next
		truncated = true
	}
	if actual == start && storedTruncated {
		truncated = true
	}
	body := data[int(actual-start):]
	writeJSON(response, http.StatusOK, runLogResponse{
		Offset: actual, NextOffset: next, Data: string(body), Truncated: truncated, Done: terminalRunState(state),
	})
}
