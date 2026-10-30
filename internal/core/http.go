package core

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/demo"
)

const leaseHeader = "X-GK80-UI-Lease"

type commandReader interface {
	Command(context.Context, uuid.UUID) (contract.CommandStatus, error)
}

type coreHTTP struct {
	secret         string
	handshake      http.Handler
	validator      *contract.InputValidator
	leases         *uiLeases
	feed           *eventFeed
	engine         *demo.Engine
	store          commandReader
	logger         *slog.Logger
	pendingMu      sync.Mutex
	pending        map[*createAttempt]struct{}
	pendingVersion uint64
	recoveryMu     sync.Mutex
	recovering     bool
	workerMu       sync.Mutex
	workers        int
	workersClosing bool
	workersDone    chan struct{}
}

func (h *coreHTTP) beginWorker() bool {
	h.workerMu.Lock()
	defer h.workerMu.Unlock()
	if h.workersClosing || h.workers >= 32 {
		return false
	}
	h.workers++
	return true
}

func (h *coreHTTP) endWorker() {
	h.workerMu.Lock()
	defer h.workerMu.Unlock()
	h.workers--
	if h.workersClosing && h.workers == 0 {
		close(h.workersDone)
	}
}

func (h *coreHTTP) stopWorkers() <-chan struct{} {
	h.workerMu.Lock()
	defer h.workerMu.Unlock()
	if !h.workersClosing {
		h.workersClosing = true
		h.workersDone = make(chan struct{})
		if h.workers == 0 {
			close(h.workersDone)
		}
	}
	return h.workersDone
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
		owner, ok := h.requestOwner(w, r)
		if !ok {
			return
		}
		if !h.recoverSnapshot(w, r, owner) {
			return
		}
		h.pendingMu.Lock()
		if h.admittedCreateLocked() || h.engine.HasUnresolvedCreate() {
			h.pendingMu.Unlock()
			writeAPIError(w, http.StatusServiceUnavailable, "state_unavailable", "State is not yet confirmed")
			return
		}
		version := h.pendingVersion
		h.pendingMu.Unlock()
		snapshot, err := h.leases.snapshot(owner.id, h.feed)
		h.pendingMu.Lock()
		unstable := version != h.pendingVersion || h.admittedCreateLocked() || h.engine.HasUnresolvedCreate()
		h.pendingMu.Unlock()
		if unstable {
			writeAPIError(w, http.StatusServiceUnavailable, "state_unavailable", "State is not yet confirmed")
			return
		}
		if err != nil {
			writeAPIError(w, http.StatusForbidden, "lease_invalid", "UI lease required")
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	case "/v1/tests":
		h.createTest(w, r)
	default:
		if strings.HasPrefix(r.URL.Path, "/v1/commands/") {
			h.commandStatus(w, r)
		} else if strings.HasPrefix(r.URL.Path, "/v1/tests/") && strings.HasSuffix(r.URL.Path, "/readiness") {
			h.readiness(w, r)
		} else {
			http.NotFound(w, r)
		}
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
