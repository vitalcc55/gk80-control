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
	"github.com/vitalcc55/gk80-control/internal/storage"
)

// lookupCreate checks a saved command without reissuing Create.
func (h *coreHTTP) lookupCreate(w http.ResponseWriter, r *http.Request) {
	closeUnread := func() {
		w.Header().Set("Connection", "close")
		_ = http.NewResponseController(w).SetReadDeadline(time.Now())
	}
	if r.Method != http.MethodPost {
		closeUnread()
		w.Header().Set("Allow", "POST")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	w.Header().Set("Connection", "close")
	owner, ok := h.requestOwner(w, r)
	if !ok {
		closeUnread()
		return
	}
	w.Header().Del("Connection")
	values := r.Header.Values("X-GK80-Database-Id")
	if len(values) != 1 {
		closeUnread()
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid request")
		return
	}
	database, err := uuid.Parse(values[0])
	if err != nil || database == uuid.Nil || !strings.EqualFold(database.String(), values[0]) {
		closeUnread()
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid request")
		return
	}
	if h.exact == nil {
		closeUnread()
		writeAPIError(w, http.StatusServiceUnavailable, "state_unavailable", "State is not yet confirmed")
		return
	}
	if database != h.exact.DatabaseID() {
		closeUnread()
		writeAPIError(w, http.StatusConflict, "database_mismatch", "Database does not match")
		return
	}
	if !h.beginWorker() {
		closeUnread()
		writeAPIError(w, http.StatusServiceUnavailable, "state_unavailable", "State is not yet confirmed")
		return
	}
	responseDone := make(chan struct{})
	workerStarted := false
	defer func() {
		close(responseDone)
		if !workerStarted {
			h.endWorker()
		}
	}()
	ctx, cancel := h.readContext(r, owner)
	defer cancel()
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	if err := controller.SetReadDeadline(deadline); err != nil {
		closeUnread()
		writeHistoryJSON(w, http.StatusServiceUnavailable, contract.ApiError{Code: "state_unavailable", Message: "State is not yet confirmed"})
		return
	}
	if err := controller.SetWriteDeadline(deadline); err != nil {
		closeUnread()
		writeHistoryJSON(w, http.StatusServiceUnavailable, contract.ApiError{Code: "state_unavailable", Message: "State is not yet confirmed"})
		return
	}
	callbackDone := make(chan struct{})
	stopCallback := context.AfterFunc(ctx, func() {
		_ = controller.SetReadDeadline(time.Now())
		_ = controller.SetWriteDeadline(time.Now())
		close(callbackDone)
	})
	defer func() {
		if !stopCallback() {
			<-callbackDone
		}
	}()
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		closeUnread()
		writeHistoryJSON(w, http.StatusUnsupportedMediaType, contract.ApiError{Code: "unsupported_media_type", Message: "JSON content type required"})
		return
	}
	mediaType, _, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" {
		closeUnread()
		writeHistoryJSON(w, http.StatusUnsupportedMediaType, contract.ApiError{Code: "unsupported_media_type", Message: "JSON content type required"})
		return
	}
	body, readErr := io.ReadAll(io.LimitReader(r.Body, 8193))
	var command contract.CreateTestCommand
	input, inspectErr := h.validator.Inspect("CreateTestCommand", body, &command)
	if !h.leases.valid(owner) {
		writeHistoryJSON(w, http.StatusForbidden, contract.ApiError{Code: "lease_invalid", Message: "UI lease required"})
		return
	}
	if ctx.Err() != nil {
		writeHistoryJSON(w, http.StatusServiceUnavailable, contract.ApiError{Code: "state_unavailable", Message: "State is not yet confirmed"})
		return
	}
	if readErr != nil || len(body) > 8192 || inspectErr != nil || command.CommandId == uuid.Nil {
		closeUnread()
		writeHistoryJSON(w, http.StatusBadRequest, contract.ApiError{Code: "invalid_request", Message: "Invalid request"})
		return
	}
	type result struct {
		status contract.CommandStatus
		err    error
	}
	done := make(chan result, 1)
	workerStarted = true
	go func() {
		defer h.endWorker()
		status, err := h.exact.LookupCreateExact(ctx, command, input.Identity)
		done <- result{status: status, err: err}
		<-responseDone
	}()
	var value result
	select {
	case value = <-done:
	case <-ctx.Done():
		value.err = ctx.Err()
	}
	if !h.leases.valid(owner) {
		writeHistoryJSON(w, http.StatusForbidden, contract.ApiError{Code: "lease_invalid", Message: "UI lease required"})
		return
	}
	if ctx.Err() != nil {
		writeHistoryJSON(w, http.StatusServiceUnavailable, contract.ApiError{Code: "state_unavailable", Message: "State is not yet confirmed"})
		return
	}
	if errors.Is(value.err, storage.ErrCommandConflict) {
		writeHistoryJSON(w, http.StatusConflict, contract.ApiError{Code: "command_conflict", Message: "Command identifier conflict"})
		return
	}
	if errors.Is(value.err, storage.ErrCommandNotFound) {
		writeHistoryJSON(w, http.StatusOK, contract.CommandStatus{CommandId: command.CommandId, Status: contract.CommandStatusStatusUnknown})
		return
	}
	if value.status.CommandId != command.CommandId || !value.status.Status.Valid() || (value.err != nil && !knownStoredRejection(value.err)) {
		writeHistoryJSON(w, http.StatusServiceUnavailable, contract.ApiError{Code: "state_unavailable", Message: "State is not yet confirmed"})
		return
	}
	writeHistoryJSON(w, http.StatusOK, value.status)
}

func knownStoredRejection(err error) bool {
	return errors.Is(err, storage.ErrRevision) || errors.Is(err, storage.ErrActiveTest) ||
		errors.Is(err, storage.ErrInvalidCommand) || errors.Is(err, storage.ErrInvalidState) ||
		errors.Is(err, storage.ErrActionMismatch) || errors.Is(err, storage.ErrTestNotFound) || errors.Is(err, storage.ErrNotReady)
}
