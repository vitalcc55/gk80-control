package core

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/demo"
	"github.com/vitalcc55/gk80-control/internal/diagnostics"
	"github.com/vitalcc55/gk80-control/internal/storage"
)

func (h *coreHTTP) requestOwner(w http.ResponseWriter, r *http.Request) (*uiLease, bool) {
	id, present, err := parseLeaseHeader(r)
	if err == nil && present {
		owner, err := h.leases.owner(id)
		if err == nil {
			return owner, true
		}
	}
	writeAPIError(w, http.StatusForbidden, "lease_invalid", "UI lease required")
	return nil, false
}

func (h *coreHTTP) readContext(r *http.Request, owner *uiLease) (context.Context, func()) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	stop := context.AfterFunc(owner.controller.Context(), cancel)
	return ctx, func() { stop(); cancel() }
}

func (h *coreHTTP) recoverSnapshot(w http.ResponseWriter, r *http.Request, owner *uiLease) bool {
	if !h.engine.NeedsRefresh() {
		return true
	}
	ctx, cancel := h.readContext(r, owner)
	defer cancel()
	h.recoveryMu.Lock()
	busy := h.recovering
	if !busy {
		h.recovering = true
	}
	h.recoveryMu.Unlock()
	var err error
	if busy {
		err = demo.ErrBusy
	} else if !h.beginWorker() {
		h.recoveryMu.Lock()
		h.recovering = false
		h.recoveryMu.Unlock()
		err = demo.ErrBusy
	} else {
		done := make(chan error, 1)
		go func() {
			defer h.endWorker()
			result := h.engine.Refresh(ctx)
			h.recoveryMu.Lock()
			h.recovering = false
			h.recoveryMu.Unlock()
			done <- result
		}()
		select {
		case err = <-done:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	if !h.leases.valid(owner) {
		writeAPIError(w, http.StatusForbidden, "lease_invalid", "UI lease required")
		return false
	}
	if err != nil && h.engine.HasUnresolvedCreate() {
		writeAPIError(w, http.StatusServiceUnavailable, "state_unavailable", "State is not yet confirmed")
		return false
	}
	return true
}

type createAttempt struct {
	id       uuid.UUID
	admitted bool
}

func (h *coreHTTP) beginCreate(id uuid.UUID) (*createAttempt, error) {
	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()
	if len(h.pending) >= 32 {
		return nil, demo.ErrBusy
	}
	if h.pending == nil {
		h.pending = make(map[*createAttempt]struct{})
	}
	attempt := &createAttempt{id: id}
	h.pending[attempt] = struct{}{}
	h.pendingVersion++
	return attempt, nil
}

func (h *coreHTTP) endCreate(attempt *createAttempt) {
	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()
	if _, exists := h.pending[attempt]; exists {
		delete(h.pending, attempt)
		h.pendingVersion++
	}
}

func (h *coreHTTP) abandonCreate(attempt *createAttempt) {
	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()
	if _, exists := h.pending[attempt]; exists && (!attempt.admitted || !h.engine.CreatePending(attempt.id)) {
		delete(h.pending, attempt)
		h.pendingVersion++
	}
}

func (h *coreHTTP) admitCreate(ctx context.Context, attempt *createAttempt) bool {
	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()
	if _, exists := h.pending[attempt]; !exists || ctx.Err() != nil {
		return false
	}
	attempt.admitted = true
	h.pendingVersion++
	return true
}

func (h *coreHTTP) createObservation(id uuid.UUID) (bool, uint64) {
	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()
	for attempt := range h.pending {
		if attempt.id == id {
			return true, h.pendingVersion
		}
	}
	return false, h.pendingVersion
}

func (h *coreHTTP) admittedCreateLocked() bool {
	for attempt := range h.pending {
		if attempt.admitted {
			return true
		}
	}
	return false
}

func (h *coreHTTP) anyCreateInFlight() bool {
	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()
	return h.admittedCreateLocked()
}

func (h *coreHTTP) createTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	owner, ok := h.requestOwner(w, r)
	if !ok {
		return
	}
	types := r.Header.Values("Content-Type")
	if len(types) != 1 {
		writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "JSON content type required")
		return
	}
	mediaType, _, err := mime.ParseMediaType(types[0])
	if err != nil || mediaType != "application/json" {
		writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "JSON content type required")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8193))
	var command contract.CreateTestCommand
	input, inspectErr := h.validator.Inspect("CreateTestCommand", body, &command)
	if err != nil || len(body) > 8192 || inspectErr != nil || command.CommandId == uuid.Nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid request")
		return
	}
	if !h.leases.valid(owner) {
		writeAPIError(w, http.StatusForbidden, "lease_invalid", "UI lease required")
		return
	}
	attempt, err := h.beginCreate(command.CommandId)
	if err != nil {
		if !h.leases.valid(owner) {
			writeAPIError(w, http.StatusForbidden, "lease_invalid", "UI lease required")
			return
		}
		h.commandResult(w, contract.CommandStatus{}, err)
		return
	}
	if !h.beginWorker() {
		h.endCreate(attempt)
		writeAPIError(w, http.StatusServiceUnavailable, "state_unavailable", "State is not yet confirmed")
		return
	}
	ctx, cancel := h.readContext(r, owner)
	defer cancel()
	type createResult struct {
		status  contract.CommandStatus
		err     error
		invalid bool
	}
	done := make(chan createResult, 1)
	go func() {
		defer h.endWorker()
		var response createResult
		defer func() {
			h.endCreate(attempt)
			done <- response
		}()
		result, err := owner.controller.LookupCreateExact(ctx, command, input.Identity)
		if !errors.Is(err, storage.ErrCommandNotFound) {
			if errors.Is(err, storage.ErrCommandConflict) {
				h.logConflict(command.CommandId)
			} else {
				h.logObserved(result)
			}
			response = createResult{status: result, err: err}
			return
		}
		if !input.Valid {
			response = createResult{invalid: true}
			return
		}
		if !h.admitCreate(ctx, attempt) {
			response.err = ctx.Err()
			return
		}
		var replayed bool
		result, replayed, err = owner.controller.CreateExactWithDisposition(ctx, command, input.Identity)
		if errors.Is(err, storage.ErrCommandConflict) {
			h.logConflict(command.CommandId)
		}
		if replayed {
			h.logObserved(result)
		} else {
			h.logCommand(result)
		}
		response = createResult{status: result, err: err}
	}()
	var resolved createResult
	select {
	case resolved = <-done:
	case <-ctx.Done():
		h.abandonCreate(attempt)
		resolved.err = ctx.Err()
	}
	if !h.leases.valid(owner) {
		writeAPIError(w, http.StatusForbidden, "lease_invalid", "UI lease required")
		return
	}
	if resolved.invalid {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid request")
		return
	}
	h.commandResult(w, resolved.status, resolved.err)
}

func (h *coreHTTP) logCommand(status contract.CommandStatus) {
	if h.logger == nil || status.CommandId == uuid.Nil {
		return
	}
	var event diagnostics.Event
	switch status.Status {
	case "accepted":
		event = diagnostics.CommandAccepted
	case "completed":
		event = diagnostics.CommandCompleted
	case "rejected":
		event = diagnostics.CommandRejected
	default:
		return
	}
	args := []any{"command_id", status.CommandId}
	if status.TestId != nil {
		args = append(args, "test_id", *status.TestId)
	}
	if status.Reason != nil {
		args = append(args, "error_code", *status.Reason)
	}
	h.logger.Info(string(event), args...)
}

func (h *coreHTTP) logConflict(id uuid.UUID) {
	if h.logger != nil {
		h.logger.Info(string(diagnostics.CommandRejected), "command_id", id, "error_code", "command_id_conflict")
	}
}

func (h *coreHTTP) logObserved(status contract.CommandStatus) {
	if h.logger == nil || status.CommandId == uuid.Nil || !status.Status.Valid() {
		return
	}
	args := []any{"command_id", status.CommandId, "command_status", string(status.Status)}
	if status.TestId != nil {
		args = append(args, "test_id", *status.TestId)
	}
	if status.Reason != nil {
		args = append(args, "error_code", *status.Reason)
	}
	h.logger.Info(string(diagnostics.CommandObserved), args...)
}

func (h *coreHTTP) commandResult(w http.ResponseWriter, status contract.CommandStatus, err error) {
	if errors.Is(err, storage.ErrCommandConflict) {
		writeAPIError(w, http.StatusConflict, "command_conflict", "Command identifier conflicts with a previous request")
		return
	}
	if status.CommandId != uuid.Nil && status.Status.Valid() {
		if status.Status == "accepted" {
			writeJSON(w, http.StatusAccepted, contract.CommandAccepted{CommandId: status.CommandId, Status: string(status.Status), TestId: status.TestId, Reason: status.Reason})
		} else {
			writeJSON(w, http.StatusOK, status)
		}
		return
	}
	if errors.Is(err, demo.ErrBusy) {
		writeAPIError(w, http.StatusConflict, "busy", "Another command is being processed")
		return
	}
	if errors.Is(err, storage.ErrInvalidCommand) {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid request")
		return
	}
	writeAPIError(w, http.StatusServiceUnavailable, "outcome_unknown", "Command outcome is not confirmed; read state")
}

func (h *coreHTTP) commandStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	owner, ok := h.requestOwner(w, r)
	if !ok {
		return
	}
	id, err := parsePathID(strings.TrimPrefix(r.URL.Path, "/v1/commands/"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid request")
		return
	}
	state := h.engine.State()
	if state.Stop != nil && state.Stop.CommandId == id {
		if !h.leases.valid(owner) {
			writeAPIError(w, http.StatusForbidden, "lease_invalid", "UI lease required")
			return
		}
		writeJSON(w, http.StatusOK, state.Stop)
		return
	}
	ctx, cancel := h.readContext(r, owner)
	defer cancel()
	if !h.beginWorker() {
		writeAPIError(w, http.StatusServiceUnavailable, "state_unavailable", "State is not yet confirmed")
		return
	}
	type readResult struct {
		status contract.CommandStatus
		err    error
	}
	done := make(chan readResult, 1)
	go func() {
		defer h.endWorker()
		status, err := h.readCommand(ctx, id)
		done <- readResult{status: status, err: err}
	}()
	var result readResult
	select {
	case result = <-done:
	case <-ctx.Done():
		result.err = ctx.Err()
	}
	status, err := result.status, result.err
	if !h.leases.valid(owner) {
		writeAPIError(w, http.StatusForbidden, "lease_invalid", "UI lease required")
		return
	}
	if errors.Is(err, storage.ErrCommandNotFound) {
		writeAPIError(w, http.StatusNotFound, "command_not_found", "Command not found")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "state_unavailable", "State is not yet confirmed")
		return
	}
	h.logObserved(status)
	writeJSON(w, http.StatusOK, status)
}

func (h *coreHTTP) readCommand(ctx context.Context, id uuid.UUID) (contract.CommandStatus, error) {
	for {
		wasPending, version := h.createObservation(id)
		wasPending = wasPending || h.engine.CreatePending(id)
		status, err := h.store.Command(ctx, id)
		if !errors.Is(err, storage.ErrCommandNotFound) {
			return status, err
		}
		nowPending, currentVersion := h.createObservation(id)
		if wasPending || nowPending || h.engine.CreatePending(id) {
			return contract.CommandStatus{CommandId: id, Status: "unknown"}, nil
		}
		if version == currentVersion {
			return contract.CommandStatus{}, err
		}
		if err := ctx.Err(); err != nil {
			return contract.CommandStatus{}, err
		}
	}
}

func (h *coreHTTP) readiness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	owner, ok := h.requestOwner(w, r)
	if !ok {
		return
	}
	if h.anyCreateInFlight() {
		writeAPIError(w, http.StatusServiceUnavailable, "state_unavailable", "State is not yet confirmed")
		return
	}
	if !h.recoverSnapshot(w, r, owner) {
		return
	}
	if h.anyCreateInFlight() || h.engine.HasUnresolvedCreate() {
		writeAPIError(w, http.StatusServiceUnavailable, "state_unavailable", "State is not yet confirmed")
		return
	}
	path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/tests/"), "/readiness")
	id, err := parsePathID(path)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid request")
		return
	}
	result, err := h.engine.Readiness(id)
	if !h.leases.valid(owner) {
		writeAPIError(w, http.StatusForbidden, "lease_invalid", "UI lease required")
		return
	}
	if errors.Is(err, storage.ErrTestNotFound) {
		writeAPIError(w, http.StatusNotFound, "test_not_found", "Test not found")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "state_unavailable", "State is not yet confirmed")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func parsePathID(value string) (uuid.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil || !strings.EqualFold(id.String(), value) {
		return uuid.Nil, storage.ErrInvalidCommand
	}
	return id, nil
}
