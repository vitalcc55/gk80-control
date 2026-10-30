package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/storage"
)

type historyReader interface {
	ListTests(context.Context, *uuid.UUID, int) ([]contract.TestRecord, *uuid.UUID, error)
	Test(context.Context, uuid.UUID) (contract.TestRecord, error)
	MeasurementsPage(context.Context, uuid.UUID, int64, int) (contract.MeasurementPage, error)
	AuditPage(context.Context, uuid.UUID, int64, int) (contract.AuditPage, error)
}

// History reads are bounded and remain tracked until the underlying reader returns.
func (h *coreHTTP) readHistory(w http.ResponseWriter, r *http.Request, owner *uiLease, read func(context.Context) (any, error)) {
	ctx, cancel := h.readContext(r, owner)
	defer cancel()
	if !h.beginWorker() {
		writeAPIError(w, http.StatusServiceUnavailable, "state_unavailable", "State is not yet confirmed")
		return
	}
	type result struct {
		value any
		err   error
	}
	done := make(chan result, 1)
	responseDone := make(chan struct{})
	defer close(responseDone)
	go func() {
		defer h.endWorker()
		value, err := read(ctx)
		done <- result{value, err}
		<-responseDone
	}()
	var response result
	select {
	case response = <-done:
	case <-ctx.Done():
		response.err = ctx.Err()
	}
	if !h.leases.valid(owner) {
		writeHistoryJSON(w, http.StatusForbidden, contract.ApiError{Code: "lease_invalid", Message: "UI lease required"})
		return
	}
	if ctx.Err() != nil {
		response.err = ctx.Err()
	}
	if errors.Is(response.err, storage.ErrTestNotFound) {
		writeHistoryJSON(w, http.StatusNotFound, contract.ApiError{Code: "test_not_found", Message: "Test not found"})
		return
	}
	if response.err != nil {
		writeHistoryJSON(w, http.StatusServiceUnavailable, contract.ApiError{Code: "state_unavailable", Message: "State is not yet confirmed"})
		return
	}
	writeHistoryJSON(w, http.StatusOK, response.value)
}

func writeHistoryJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		status = http.StatusServiceUnavailable
		body = []byte(`{"code":"state_unavailable","message":"State is not yet confirmed"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if _, err := w.Write(body); err == nil {
		_ = http.NewResponseController(w).Flush()
	}
}

func historyQuery(r *http.Request, paginated bool) (url.Values, int, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, 0, err
	}
	for key, entries := range values {
		if !paginated || (key != "after" && key != "limit") || len(entries) != 1 || entries[0] == "" {
			return nil, 0, storage.ErrInvalidCommand
		}
	}
	limit := 100
	if value := values.Get("limit"); value != "" {
		n, err := nonnegativeInteger(value)
		if err != nil || n < 1 || n > 500 {
			return nil, 0, storage.ErrInvalidCommand
		}
		limit = int(n)
	}
	return values, limit, nil
}

func nonnegativeInteger(value string) (int64, error) {
	if value == "" || strings.Trim(value, "0123456789") != "" {
		return 0, storage.ErrInvalidCommand
	}
	return strconv.ParseInt(value, 10, 64)
}

func (h *coreHTTP) historyList(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.requestOwner(w, r)
	if !ok {
		return
	}
	values, limit, err := historyQuery(r, true)
	var after *uuid.UUID
	if err == nil && values.Get("after") != "" {
		var id uuid.UUID
		id, err = parsePathID(values.Get("after"))
		after = &id
	}
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid request")
		return
	}
	h.readHistory(w, r, owner, func(ctx context.Context) (any, error) {
		items, next, err := h.history.ListTests(ctx, after, limit)
		return contract.TestList{Items: items, NextAfter: next}, err
	})
}

func (h *coreHTTP) historyTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	owner, ok := h.requestOwner(w, r)
	if !ok {
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/tests/"), "/")
	if len(parts) > 2 || (len(parts) == 2 && parts[1] != "measurements" && parts[1] != "audit") {
		http.NotFound(w, r)
		return
	}
	id, err := parsePathID(parts[0])
	values, limit, queryErr := historyQuery(r, len(parts) == 2)
	var after int64
	if queryErr == nil && values.Get("after") != "" {
		after, queryErr = nonnegativeInteger(values.Get("after"))
	}
	if err != nil || queryErr != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid request")
		return
	}
	h.readHistory(w, r, owner, func(ctx context.Context) (any, error) {
		if len(parts) == 1 {
			return h.history.Test(ctx, id)
		}
		if parts[1] == "measurements" {
			return h.history.MeasurementsPage(ctx, id, after, limit)
		}
		return h.history.AuditPage(ctx, id, after, limit)
	})
}
