package core

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/storage"
)

func (h *coreHTTP) testAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	owner, ok := h.requestOwner(w, r)
	if !ok {
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/tests/"), "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	id, err := parsePathID(parts[0])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid request")
		return
	}
	if len(parts) == 2 && parts[1] == "stop" {
		h.stopTest(w, r, owner, id)
		return
	}
	var actionID uuid.UUID
	kind := ""
	if len(parts) == 2 && parts[1] == "start" {
		kind = "start"
	} else if len(parts) == 4 && parts[1] == "actions" && parts[3] == "confirm" {
		actionID, err = parsePathID(parts[2])
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid request")
			return
		}
		kind = "confirm"
	} else {
		http.NotFound(w, r)
		return
	}
	var input contract.InspectedInput
	var commandID uuid.UUID
	var lookup func(context.Context) (contract.CommandStatus, error)
	var execute func(context.Context) (contract.CommandStatus, bool, error)
	if kind == "start" {
		var command contract.StartTestCommand
		input, ok = h.inspectBody(w, r, "StartTestCommand", &command)
		commandID = command.CommandId
		lookup = func(ctx context.Context) (contract.CommandStatus, error) {
			return owner.controller.LookupStart(ctx, id, command)
		}
		execute = func(ctx context.Context) (contract.CommandStatus, bool, error) {
			return owner.controller.StartWithDisposition(ctx, id, command)
		}
	} else {
		var command contract.ConfirmActionCommand
		input, ok = h.inspectBody(w, r, "ConfirmActionCommand", &command)
		commandID = command.CommandId
		lookup = func(ctx context.Context) (contract.CommandStatus, error) {
			return owner.controller.LookupConfirm(ctx, id, actionID, command)
		}
		execute = func(ctx context.Context) (contract.CommandStatus, bool, error) {
			return owner.controller.ConfirmWithDisposition(ctx, id, actionID, command)
		}
	}
	if !ok {
		return
	}
	if commandID == uuid.Nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid request")
		return
	}
	if !h.leases.valid(owner) {
		writeAPIError(w, http.StatusForbidden, "lease_invalid", "UI lease required")
		return
	}
	attempt, err := h.beginAttempt(commandID, kind)
	if err != nil {
		if !h.leases.valid(owner) {
			writeAPIError(w, http.StatusForbidden, "lease_invalid", "UI lease required")
			return
		}
		h.commandResult(w, contract.CommandStatus{}, err)
		return
	}
	if !h.beginWorker() {
		h.endAttempt(attempt)
		writeAPIError(w, http.StatusServiceUnavailable, "state_unavailable", "State is not yet confirmed")
		return
	}
	ctx, cancel := h.readContext(r, owner)
	defer cancel()
	type outcome struct {
		status  contract.CommandStatus
		err     error
		invalid bool
	}
	done := make(chan outcome, 1)
	go func() {
		defer h.endWorker()
		var result outcome
		defer func() { h.endAttempt(attempt); done <- result }()
		status, err := lookup(ctx)
		if !errors.Is(err, storage.ErrCommandNotFound) {
			if errors.Is(err, storage.ErrCommandConflict) {
				h.logConflict(commandID, id, actionID)
			} else if kind == "confirm" {
				h.logObserved(status, actionID)
			} else {
				h.logObserved(status)
			}
			result = outcome{status: status, err: err}
			return
		}
		if !input.Valid {
			result.invalid = true
			return
		}
		if !h.admitAttempt(ctx, attempt) {
			result.err = ctx.Err()
			return
		}
		status, replayed, err := execute(ctx)
		if errors.Is(err, storage.ErrCommandConflict) {
			h.logConflict(commandID, id, actionID)
		}
		if replayed {
			if kind == "confirm" {
				h.logObserved(status, actionID)
			} else {
				h.logObserved(status)
			}
		} else if kind == "confirm" && status.Status != "completed" {
			h.logCommand(status, actionID)
		} else if kind == "start" && status.Status != "accepted" {
			h.logCommand(status)
		}
		result = outcome{status: status, err: err}
	}()
	var result outcome
	select {
	case result = <-done:
	case <-ctx.Done():
		h.abandonAttempt(attempt)
		result.err = ctx.Err()
	}
	if !h.leases.valid(owner) {
		writeAPIError(w, http.StatusForbidden, "lease_invalid", "UI lease required")
		return
	}
	if result.invalid {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid request")
		return
	}
	h.commandResult(w, result.status, result.err)
}

func (h *coreHTTP) inspectBody(w http.ResponseWriter, r *http.Request, name string, target any) (contract.InspectedInput, bool) {
	types := r.Header.Values("Content-Type")
	if len(types) != 1 {
		writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "JSON content type required")
		return contract.InspectedInput{}, false
	}
	mediaType, _, err := mime.ParseMediaType(types[0])
	if err != nil || mediaType != "application/json" {
		writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "JSON content type required")
		return contract.InspectedInput{}, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8193))
	input, inspectErr := h.validator.Inspect(name, body, target)
	if err != nil || len(body) > 8192 || inspectErr != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid request")
		return contract.InspectedInput{}, false
	}
	return input, true
}

func (h *coreHTTP) stopTest(w http.ResponseWriter, r *http.Request, owner *uiLease, id uuid.UUID) {
	var command contract.StopTestCommand
	input, ok := h.inspectBody(w, r, "StopTestCommand", &command)
	if !ok {
		return
	}
	if !input.Valid || command.CommandId == uuid.Nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid request")
		return
	}
	if !h.leases.valid(owner) {
		writeAPIError(w, http.StatusForbidden, "lease_invalid", "UI lease required")
		return
	}
	status, matched, replayed, err := owner.controller.TryStop(id, command.CommandId)
	if matched {
		if replayed {
			h.logObserved(status)
		}
		if !h.leases.valid(owner) {
			writeAPIError(w, http.StatusForbidden, "lease_invalid", "UI lease required")
			return
		}
		h.commandResult(w, status, err)
		return
	}
	if !h.beginWorker() {
		writeAPIError(w, http.StatusServiceUnavailable, "state_unavailable", "State is not yet confirmed")
		return
	}
	ctx, cancel := h.readContext(r, owner)
	defer cancel()
	type outcome struct {
		status contract.CommandStatus
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		defer h.endWorker()
		status, err := owner.controller.LookupStop(ctx, id, command.CommandId)
		done <- outcome{status: status, err: err}
	}()
	var result outcome
	select {
	case result = <-done:
	case <-ctx.Done():
		result.err = ctx.Err()
	}
	if !h.leases.valid(owner) {
		writeAPIError(w, http.StatusForbidden, "lease_invalid", "UI lease required")
		return
	}
	if errors.Is(result.err, storage.ErrCommandConflict) {
		h.logConflict(command.CommandId, id)
	} else {
		h.logObserved(result.status)
	}
	h.commandResult(w, result.status, result.err)
}
