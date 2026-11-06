package browser

import (
	"context"
	"net/http"
	"net/url"

	"github.com/vitalcc55/gk80-control/internal/contract"
)

type historyOperations interface {
	ListTests(context.Context, contract.ListTestsParams) (contract.TestList, error)
	Test(context.Context, string) (contract.TestRecord, error)
	Readiness(context.Context, string) (contract.Readiness, error)
	Measurements(context.Context, string, int64, int) (contract.MeasurementPage, error)
	Audit(context.Context, string, int64, int) (contract.AuditPage, error)
}

func pageQuery(r *http.Request) (string, int, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", 0, err
	}
	for key, entries := range values {
		if (key != "after" && key != "limit") || len(entries) != 1 || entries[0] == "" {
			return "", 0, errInvalidQuery
		}
	}
	limit := 100
	if raw := values.Get("limit"); raw != "" {
		n, err := nonnegative(raw)
		if err != nil || n < 1 || n > 500 {
			return "", 0, errInvalidQuery
		}
		limit = int(n)
	}
	return values.Get("after"), limit, nil
}

func registerHistoryRoutes(mux *http.ServeMux, s *browserSession, service historyOperations) {
	register := func(pattern string, handler http.HandlerFunc) { mux.Handle(pattern, s.protect(handler)) }
	invalid := func(w http.ResponseWriter, r *http.Request) {
		s.reply(w, r, http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}
	register("GET /api/tests", func(w http.ResponseWriter, r *http.Request) {
		values, err := url.ParseQuery(r.URL.RawQuery)
		params := contract.ListTestsParams{}
		if err == nil {
			params, err = contract.ParseTestListQuery(values)
		}
		if err != nil {
			invalid(w, r)
			return
		}
		s.call(w, r, func(ctx context.Context) (any, error) { return service.ListTests(ctx, params) })
	})
	register("GET /api/tests/{testId}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("testId")
		if !validID(id) || r.URL.RawQuery != "" || r.URL.ForceQuery {
			invalid(w, r)
			return
		}
		s.call(w, r, func(ctx context.Context) (any, error) { return service.Test(ctx, id) })
	})
	register("GET /api/tests/{testId}/readiness", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("testId")
		if !validID(id) || r.URL.RawQuery != "" || r.URL.ForceQuery {
			invalid(w, r)
			return
		}
		s.call(w, r, func(ctx context.Context) (any, error) { return service.Readiness(ctx, id) })
	})
	register("GET /api/tests/{testId}/measurements", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("testId")
		after, limit, err := pageQuery(r)
		if !validID(id) || err != nil {
			invalid(w, r)
			return
		}
		var sequence int64
		if after != "" {
			sequence, err = nonnegative(after)
			if err != nil {
				invalid(w, r)
				return
			}
		}
		s.call(w, r, func(ctx context.Context) (any, error) { return service.Measurements(ctx, id, sequence, limit) })
	})
	register("GET /api/tests/{testId}/audit", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("testId")
		after, limit, err := pageQuery(r)
		if !validID(id) || err != nil {
			invalid(w, r)
			return
		}
		var sequence int64
		if after != "" {
			sequence, err = nonnegative(after)
			if err != nil {
				invalid(w, r)
				return
			}
		}
		s.call(w, r, func(ctx context.Context) (any, error) { return service.Audit(ctx, id, sequence, limit) })
	})
}
