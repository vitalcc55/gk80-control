package contract

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var ErrHistoryQuery = errors.New("invalid history query")

// NormalizeTestListParams validates only the read contract, not admission or saved state.
func NormalizeTestListParams(params ListTestsParams) (ListTestsParams, error) {
	if params.After != nil && *params.After == uuid.Nil {
		return params, ErrHistoryQuery
	}
	limit := 100
	if params.Limit != nil {
		limit = *params.Limit
	}
	if limit < 1 || limit > 500 {
		return params, ErrHistoryQuery
	}
	params.Limit = &limit
	if params.Q != nil {
		if !utf8.ValidString(*params.Q) || utf8.RuneCountInString(*params.Q) > 200 {
			return params, ErrHistoryQuery
		}
		query := strings.TrimSpace(*params.Q)
		params.Q = &query
	}
	if params.Outcome != nil {
		switch *params.Outcome {
		case "pending", "completed", "interrupted", "unknown":
		default:
			return params, ErrHistoryQuery
		}
	}
	var bounds [2]time.Time
	for index, raw := range []*string{params.CreatedFrom, params.CreatedBefore} {
		if raw != nil {
			instant, err := ParseHistoryTime(*raw)
			if err != nil {
				return params, ErrHistoryQuery
			}
			bounds[index] = instant
		}
	}
	if params.CreatedFrom != nil && params.CreatedBefore != nil && !bounds[0].Before(bounds[1]) {
		return params, ErrHistoryQuery
	}
	return params, nil
}

// Keep the wire lexeme until validation: time.Time decoding would silently truncate sub-nanoseconds.
func ParseHistoryTime(raw string) (time.Time, error) {
	// time.Parse accepts a single-digit hour; the wire contract requires the fixed RFC 3339 layout.
	if len(raw) < 20 || raw[4] != '-' || raw[7] != '-' || raw[10] != 'T' || raw[13] != ':' || raw[16] != ':' {
		return time.Time{}, ErrHistoryQuery
	}
	if strings.Contains(raw, ",") {
		return time.Time{}, ErrHistoryQuery
	}
	if index := strings.IndexByte(raw, '.'); index >= 0 {
		digits := 0
		for _, r := range raw[index+1:] {
			if r < '0' || r > '9' {
				break
			}
			digits++
		}
		if digits < 1 || digits > 9 {
			return time.Time{}, ErrHistoryQuery
		}
	}
	if len(raw) >= 6 && raw[len(raw)-1] != 'Z' {
		zone := raw[len(raw)-6:]
		hours, errHours := strconv.Atoi(zone[1:3])
		minutes, errMinutes := strconv.Atoi(zone[4:])
		if (zone[0] != '+' && zone[0] != '-') || zone[3] != ':' || errHours != nil || errMinutes != nil || hours > 23 || minutes > 59 {
			return time.Time{}, ErrHistoryQuery
		}
	}
	instant, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, ErrHistoryQuery
	}
	return instant, nil
}

// ParseTestListQuery is shared by the two strict HTTP read boundaries.
func ParseTestListQuery(values url.Values) (ListTestsParams, error) {
	params := ListTestsParams{}
	for key, entries := range values {
		if len(entries) != 1 || (entries[0] == "" && key != "q") {
			return params, ErrHistoryQuery
		}
		value := entries[0]
		switch key {
		case "q":
			params.Q = &value
		case "after":
			id, err := uuid.Parse(value)
			if err != nil || !strings.EqualFold(value, id.String()) {
				return params, ErrHistoryQuery
			}
			params.After = &id
		case "limit":
			if strings.Trim(value, "0123456789") != "" {
				return params, ErrHistoryQuery
			}
			limit, err := strconv.Atoi(value)
			if err != nil {
				return params, ErrHistoryQuery
			}
			params.Limit = &limit
		case "outcome":
			outcome := TestRecordOutcome(value)
			params.Outcome = &outcome
		case "createdFrom", "createdBefore":
			if key == "createdFrom" {
				params.CreatedFrom = &value
			} else {
				params.CreatedBefore = &value
			}
		default:
			return params, ErrHistoryQuery
		}
	}
	return NormalizeTestListParams(params)
}

func EncodeTestListQuery(params ListTestsParams) (url.Values, error) {
	params, err := NormalizeTestListParams(params)
	if err != nil {
		return nil, err
	}
	values := url.Values{"limit": {strconv.Itoa(*params.Limit)}}
	if params.After != nil {
		values.Set("after", params.After.String())
	}
	if params.Q != nil && *params.Q != "" {
		values.Set("q", *params.Q)
	}
	if params.Outcome != nil {
		values.Set("outcome", string(*params.Outcome))
	}
	if params.CreatedFrom != nil {
		values.Set("createdFrom", *params.CreatedFrom)
	}
	if params.CreatedBefore != nil {
		values.Set("createdBefore", *params.CreatedBefore)
	}
	return values, nil
}
