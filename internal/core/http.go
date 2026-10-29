package core

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/demo"
)

const leaseHeader = "X-GK80-UI-Lease"

type coreHTTP struct {
	secret    string
	handshake http.Handler
	validator *contract.InputValidator
	leases    *uiLeases
	feed      *eventFeed
}

func (h *coreHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/handshake" {
		h.handshake.ServeHTTP(w, r)
		return
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, "Bearer ")), []byte(h.secret)) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	switch r.URL.Path {
	case "/v1/ui-lease":
		h.lease(w, r)
	case "/v1/snapshot":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
			return
		}
		id, present, err := parseLeaseHeader(r)
		if err != nil || !present {
			writeAPIError(w, http.StatusForbidden, "lease_invalid", "UI lease required")
			return
		}
		snapshot, err := h.leases.snapshot(id, h.feed)
		if err != nil {
			writeAPIError(w, http.StatusForbidden, "lease_invalid", "UI lease required")
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	default:
		http.NotFound(w, r)
	}
}

func (h *coreHTTP) lease(w http.ResponseWriter, r *http.Request) {
	id, present, err := parseLeaseHeader(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "lease_invalid", "UI lease required")
		return
	}
	switch r.Method {
	case http.MethodPost:
		contentTypes := r.Header.Values("Content-Type")
		if len(contentTypes) != 1 {
			writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "JSON content type required")
			return
		}
		mediaType, _, err := mime.ParseMediaType(contentTypes[0])
		if err != nil || mediaType != "application/json" {
			writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "JSON content type required")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8193))
		var request contract.LeaseRequest
		if err != nil || len(body) > 8192 || h.validator.Decode("LeaseRequest", body, &request) != nil || request.ClientId == uuid.Nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid request")
			return
		}
		lease, err := h.leases.acquire(request.ClientId, id)
		if err != nil {
			h.leaseError(w, err)
			return
		}
		h.writeLease(w, lease)
	case http.MethodPut:
		if !present {
			writeAPIError(w, http.StatusForbidden, "lease_invalid", "UI lease required")
			return
		}
		lease, err := h.leases.renew(id)
		if err != nil {
			h.leaseError(w, err)
			return
		}
		h.writeLease(w, lease)
	case http.MethodDelete:
		if !present {
			writeAPIError(w, http.StatusForbidden, "lease_invalid", "UI lease required")
			return
		}
		if err := h.leases.release(id); err != nil {
			h.leaseError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "POST, PUT, DELETE")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
	}
}

func parseLeaseHeader(r *http.Request) (uuid.UUID, bool, error) {
	values := r.Header.Values(leaseHeader)
	if len(values) == 0 {
		return uuid.Nil, false, nil
	}
	if len(values) != 1 {
		return uuid.Nil, true, errLeaseInvalid
	}
	id, err := uuid.Parse(values[0])
	if err != nil || id == uuid.Nil || !strings.EqualFold(id.String(), values[0]) {
		return uuid.Nil, true, errLeaseInvalid
	}
	return id, true, nil
}

func (h *coreHTTP) writeLease(w http.ResponseWriter, lease *uiLease) {
	result, err := h.leases.view(lease)
	if err != nil {
		h.leaseError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *coreHTTP) leaseError(w http.ResponseWriter, err error) {
	if errors.Is(err, errLeaseBusy) || errors.Is(err, demo.ErrBusy) {
		writeAPIError(w, http.StatusConflict, "lease_busy", "UI lease is occupied")
		return
	}
	writeAPIError(w, http.StatusForbidden, "lease_invalid", "UI lease required")
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, contract.ApiError{Code: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
