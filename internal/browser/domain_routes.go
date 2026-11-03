package browser

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
	"github.com/vitalcc55/gk80-control/internal/desktop"
)

type domainOperations interface {
	SessionView(context.Context) (contract.SessionView, error)
	ObserveSession(context.Context) (contract.SessionView, error)
	WaitConnection(context.Context, int64, int64) (contract.ConnectionNotice, error)
	CreateTest(context.Context, desktop.CreateCommandInput) (contract.CommandDispatch, error)
	StartTest(context.Context, string, desktop.StartCommandInput) (contract.CommandDispatch, error)
	ConfirmAction(context.Context, string, string, desktop.ConfirmCommandInput) (contract.CommandDispatch, error)
	Command(context.Context, string) (contract.CommandStatus, error)
	LookupCommandExact(context.Context, string, string, string, string, string, string) (contract.ExactLookupResult, error)
}

func (s *browserSession) call(w http.ResponseWriter, r *http.Request, operation func(context.Context) (any, error)) {
	if !s.actionAllowed(r.Context()) {
		panic(http.ErrAbortHandler)
	}
	value, err := operation(r.Context())
	if err != nil {
		s.reply(w, r, http.StatusServiceUnavailable, map[string]string{"error": "operation unavailable"})
		return
	}
	s.reply(w, r, http.StatusOK, value)
}

func nonnegative(value string) (int64, error) {
	if value == "" || strings.Trim(value, "0123456789") != "" {
		return 0, errInvalidQuery
	}
	return strconv.ParseInt(value, 10, 64)
}

var errInvalidQuery = errors.New("invalid query")

func waitQuery(r *http.Request) (int64, int64, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, 0, err
	}
	var after, sequence int64
	for key, entries := range values {
		if (key != "after" && key != "afterSequence") || len(entries) != 1 {
			return 0, 0, errInvalidQuery
		}
		n, err := nonnegative(entries[0])
		if err != nil {
			return 0, 0, err
		}
		if key == "after" {
			after = n
		} else {
			sequence = n
		}
	}
	return after, sequence, nil
}

func validID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && strings.EqualFold(id.String(), value)
}

func commandBody[T any](w http.ResponseWriter, r *http.Request) (T, error) {
	var input T
	body, err := readJSON(w, r)
	if err != nil {
		return input, err
	}
	err = json.Unmarshal(body, &input)
	return input, err
}

func singleHeader(r *http.Request, name string, required bool) (string, bool) {
	values := r.Header.Values(name)
	if len(values) == 0 && !required {
		return "", true
	}
	if len(values) != 1 || values[0] == "" {
		return "", false
	}
	return values[0], true
}

func registerDomainRoutes(mux *http.ServeMux, s *browserSession, service domainOperations, bootstrap bootstrapOperations) {
	register := func(pattern string, handler http.HandlerFunc) { mux.Handle(pattern, s.protect(handler)) }
	invalid := func(w http.ResponseWriter, r *http.Request) {
		s.reply(w, r, http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}
	register("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			invalid(w, r)
			return
		}
		s.call(w, r, func(context.Context) (any, error) { return bootstrap.Status(), nil })
	})
	register("POST /api/verify", func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength != 0 || r.URL.RawQuery != "" || r.URL.ForceQuery {
			invalid(w, r)
			return
		}
		s.call(w, r, func(ctx context.Context) (any, error) { return bootstrap.Verify(ctx), nil })
	})
	register("GET /api/session/view", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			invalid(w, r)
			return
		}
		s.call(w, r, func(ctx context.Context) (any, error) { return service.SessionView(ctx) })
	})
	register("GET /api/session/observe", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			invalid(w, r)
			return
		}
		s.call(w, r, func(ctx context.Context) (any, error) { return service.ObserveSession(ctx) })
	})
	register("GET /api/session/wait", func(w http.ResponseWriter, r *http.Request) {
		after, sequence, err := waitQuery(r)
		if err != nil {
			invalid(w, r)
			return
		}
		s.call(w, r, func(ctx context.Context) (any, error) { return service.WaitConnection(ctx, after, sequence) })
	})
	register("POST /api/tests", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			invalid(w, r)
			return
		}
		input, err := commandBody[desktop.CreateCommandInput](w, r)
		if err != nil || input.CommandId == uuid.Nil {
			invalid(w, r)
			return
		}
		s.call(w, r, func(ctx context.Context) (any, error) { return service.CreateTest(ctx, input) })
	})
	register("POST /api/tests/{testId}/start", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("testId")
		if !validID(id) || r.URL.RawQuery != "" || r.URL.ForceQuery {
			invalid(w, r)
			return
		}
		input, err := commandBody[desktop.StartCommandInput](w, r)
		if err != nil || input.CommandId == uuid.Nil {
			invalid(w, r)
			return
		}
		s.call(w, r, func(ctx context.Context) (any, error) { return service.StartTest(ctx, id, input) })
	})
	register("POST /api/tests/{testId}/actions/{actionId}/confirm", func(w http.ResponseWriter, r *http.Request) {
		id, action := r.PathValue("testId"), r.PathValue("actionId")
		if !validID(id) || !validID(action) || r.URL.RawQuery != "" || r.URL.ForceQuery {
			invalid(w, r)
			return
		}
		input, err := commandBody[desktop.ConfirmCommandInput](w, r)
		if err != nil || input.CommandId == uuid.Nil {
			invalid(w, r)
			return
		}
		s.call(w, r, func(ctx context.Context) (any, error) { return service.ConfirmAction(ctx, id, action, input) })
	})
	register("GET /api/commands/{commandId}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("commandId")
		if !validID(id) || r.URL.RawQuery != "" || r.URL.ForceQuery {
			invalid(w, r)
			return
		}
		s.call(w, r, func(ctx context.Context) (any, error) { return service.Command(ctx, id) })
	})
	register("POST /api/commands/{commandId}/lookup-exact", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("commandId")
		database, dbOK := singleHeader(r, "X-GK80-Database", true)
		kind, kindOK := singleHeader(r, "X-GK80-Kind", true)
		testID, testOK := singleHeader(r, "X-GK80-Test", false)
		actionID, actionOK := singleHeader(r, "X-GK80-Action", false)
		validPath := kind == "create" && testID == "" && actionID == "" ||
			kind == "start" && validID(testID) && actionID == "" ||
			kind == "stop" && validID(testID) && actionID == "" ||
			kind == "confirm" && validID(testID) && validID(actionID)
		if !validID(id) || !dbOK || !validID(database) || !kindOK || !testOK || !actionOK || !validPath || r.URL.RawQuery != "" || r.URL.ForceQuery {
			invalid(w, r)
			return
		}
		body, err := readJSON(w, r)
		if err != nil {
			invalid(w, r)
			return
		}
		commandID := uuid.Nil
		switch kind {
		case "create":
			var input desktop.CreateCommandInput
			err = json.Unmarshal(body, &input)
			commandID = input.CommandId
		case "start":
			var input desktop.StartCommandInput
			err = json.Unmarshal(body, &input)
			commandID = input.CommandId
		case "confirm":
			var input desktop.ConfirmCommandInput
			err = json.Unmarshal(body, &input)
			commandID = input.CommandId
		case "stop":
			var input desktop.StopCommandInput
			err = json.Unmarshal(body, &input)
			commandID = input.CommandId
		}
		if err != nil || commandID == uuid.Nil || !strings.EqualFold(commandID.String(), id) {
			invalid(w, r)
			return
		}
		s.call(w, r, func(ctx context.Context) (any, error) {
			return service.LookupCommandExact(ctx, database, kind, testID, actionID, id, string(body))
		})
	})
}
