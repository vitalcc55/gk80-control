package browser

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/desktop"
)

type sessionOperations interface {
	BeginSessionCheck() (string, error)
	KeepAlive(context.Context, string) error
	StopTest(context.Context, string, desktop.StopCommandInput) (contract.CommandStatus, error)
}

type browserOperations interface {
	sessionOperations
	domainOperations
	historyOperations
}

type bootstrapOperations interface {
	Status() contract.DesktopStatus
	Verify(context.Context) contract.DesktopStatus
}

var _ browserOperations = (*desktop.Service)(nil)
var _ bootstrapOperations = (*desktop.BootstrapService)(nil)

func readJSON(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		return nil, io.ErrUnexpectedEOF
	}
	mediaType, _, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" {
		return nil, io.ErrUnexpectedEOF
	}
	deadline := time.Now().Add(2 * time.Second)
	if d, ok := r.Context().Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := http.NewResponseController(w).SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	controller := http.NewResponseController(w)
	callbackDone := make(chan struct{})
	stop := context.AfterFunc(r.Context(), func() {
		_ = controller.SetReadDeadline(time.Now())
		close(callbackDone)
	})
	defer func() {
		if !stop() {
			<-callbackDone
		}
	}()
	body, err := io.ReadAll(io.LimitReader(r.Body, 8193))
	if err != nil || len(body) > 8192 || r.Context().Err() != nil {
		return nil, io.ErrUnexpectedEOF
	}
	return body, nil
}

func (s *browserSession) reply(w http.ResponseWriter, r *http.Request, status int, value any) {
	if value == nil {
		s.writeJSON(w, r, status, nil)
		return
	}
	body, err := json.Marshal(value)
	if err != nil {
		status, body = http.StatusInternalServerError, []byte(`{"error":"response unavailable"}`)
	}
	s.writeJSON(w, r, status, body)
}

func newRoutes(s *browserSession, service browserOperations, bootstrap bootstrapOperations) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/session/stream", s.openChannel)
	registerDomainRoutes(mux, s, service, bootstrap)
	registerHistoryRoutes(mux, s, service)
	mux.Handle("POST /api/session/check", s.protectClass(heartbeatRequest, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength != 0 {
			s.reply(w, r, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}
		if !s.actionAllowed(r.Context()) {
			panic(http.ErrAbortHandler)
		}
		ticket, err := service.BeginSessionCheck()
		if err != nil {
			s.reply(w, r, http.StatusServiceUnavailable, map[string]string{"error": "session unavailable"})
			return
		}
		s.reply(w, r, http.StatusOK, map[string]string{"ticket": ticket})
	})))
	mux.Handle("POST /api/session/heartbeat", s.protectClass(heartbeatRequest, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := readJSON(w, r)
		if err != nil {
			s.reply(w, r, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}
		if _, err := contract.CanonicalJSON(body); err != nil {
			s.reply(w, r, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(body, &object); err != nil || len(object) != 1 {
			s.reply(w, r, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}
		var ticket string
		raw, exists := object["ticket"]
		if !exists || json.Unmarshal(raw, &ticket) != nil {
			s.reply(w, r, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}
		id, err := uuid.Parse(ticket)
		if err != nil || id == uuid.Nil {
			s.reply(w, r, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}
		if !s.actionAllowed(r.Context()) {
			panic(http.ErrAbortHandler)
		}
		if err := service.KeepAlive(r.Context(), ticket); err != nil {
			s.reply(w, r, http.StatusServiceUnavailable, map[string]string{"error": "session unavailable"})
			return
		}
		s.reply(w, r, http.StatusNoContent, nil)
	})))
	mux.Handle("POST /api/tests/{testId}/stop", s.protectClass(stopRequest, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("testId"))
		if err != nil || id == uuid.Nil {
			s.reply(w, r, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}
		body, err := readJSON(w, r)
		if err != nil {
			s.reply(w, r, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}
		var input desktop.StopCommandInput
		if err := json.Unmarshal(body, &input); err != nil {
			s.reply(w, r, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}
		if input.CommandId == uuid.Nil {
			s.reply(w, r, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}
		if !s.actionAllowed(r.Context()) {
			panic(http.ErrAbortHandler)
		}
		status, err := service.StopTest(r.Context(), r.PathValue("testId"), input)
		if err != nil {
			if errors.Is(err, desktop.ErrCommandConflict) {
				s.reply(w, r, http.StatusConflict, map[string]string{"error": "command_conflict"})
				return
			}
			s.reply(w, r, http.StatusServiceUnavailable, map[string]string{"error": "command outcome unavailable"})
			return
		}
		s.reply(w, r, http.StatusOK, status)
	})))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			closeUnreadRequest(w)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if path.Clean(r.URL.Path) != r.URL.Path {
			closeUnreadRequest(w)
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		if _, pattern := mux.Handler(r); pattern == "" {
			closeUnreadRequest(w)
		}
		mux.ServeHTTP(w, r)
	})
}
