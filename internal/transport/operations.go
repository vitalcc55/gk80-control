package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

const maxCoreResponseBytes = 4 << 20

// RequestError never includes a raw response, URL, credentials, or payload.
type RequestError struct {
	Status         int
	Code           string
	OutcomeUnknown bool
}

func (e *RequestError) Error() string { return "core request not confirmed" }

var responseValidation struct {
	once      sync.Once
	validator *contract.ResponseValidator
	err       error
}

func validateResponse(name string, body []byte, target any) error {
	responseValidation.once.Do(func() { responseValidation.validator, responseValidation.err = contract.NewResponseValidator() })
	if responseValidation.err != nil {
		return responseValidation.err
	}
	return responseValidation.validator.Decode(name, body, target)
}

func invalidRequest() *RequestError { return &RequestError{Code: "invalid_request"} }

func (c *Client) exchange(ctx context.Context, method, path string, lease uuid.UUID, payload any) (int, []byte, *RequestError) {
	mutating := method != http.MethodGet
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return 0, nil, invalidRequest()
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, invalidRequest()
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	if lease != uuid.Nil {
		req.Header.Set("X-GK80-UI-Lease", lease.String())
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(req)
	if err != nil {
		return 0, nil, &RequestError{Code: "unavailable", OutcomeUnknown: mutating}
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxCoreResponseBytes+1))
	if err != nil || len(data) > maxCoreResponseBytes {
		return response.StatusCode, nil, &RequestError{Status: response.StatusCode, Code: "invalid_response", OutcomeUnknown: mutating}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var apiError contract.ApiError
		code := "invalid_response"
		if validateResponse("ApiError", data, &apiError) == nil {
			code = apiError.Code
		}
		unknown := mutating && (response.StatusCode == http.StatusForbidden || response.StatusCode >= 500 || code == "invalid_response")
		return response.StatusCode, nil, &RequestError{Status: response.StatusCode, Code: code, OutcomeUnknown: unknown}
	}
	return response.StatusCode, data, nil
}

func (c *Client) get(ctx context.Context, path string, lease uuid.UUID, schema string, target any) error {
	if lease == uuid.Nil {
		return invalidRequest()
	}
	status, data, failure := c.exchange(ctx, http.MethodGet, path, lease, nil)
	if failure != nil {
		return failure
	}
	if status != http.StatusOK || validateResponse(schema, data, target) != nil {
		return &RequestError{Status: status, Code: "invalid_response"}
	}
	return nil
}

func (c *Client) AcquireLease(ctx context.Context, clientID uuid.UUID, proof *uuid.UUID) (contract.UiLease, error) {
	var result contract.UiLease
	if clientID == uuid.Nil || (proof != nil && *proof == uuid.Nil) {
		return result, invalidRequest()
	}
	lease := uuid.Nil
	if proof != nil {
		lease = *proof
	}
	status, data, failure := c.exchange(ctx, http.MethodPost, "/v1/ui-lease", lease, contract.LeaseRequest{ClientId: clientID})
	if failure != nil {
		return result, failure
	}
	if status != http.StatusOK || validateResponse("UiLease", data, &result) != nil || result.ClientId != clientID || result.LeaseId == uuid.Nil || (proof != nil && result.LeaseId != *proof) {
		return contract.UiLease{}, &RequestError{Status: status, Code: "invalid_response", OutcomeUnknown: true}
	}
	return result, nil
}

func (c *Client) RenewLease(ctx context.Context, lease uuid.UUID) (contract.UiLease, error) {
	var result contract.UiLease
	if lease == uuid.Nil {
		return result, invalidRequest()
	}
	status, data, failure := c.exchange(ctx, http.MethodPut, "/v1/ui-lease", lease, nil)
	if failure != nil {
		return result, failure
	}
	if status != http.StatusOK || validateResponse("UiLease", data, &result) != nil || result.LeaseId != lease || result.ClientId == uuid.Nil {
		return contract.UiLease{}, &RequestError{Status: status, Code: "invalid_response", OutcomeUnknown: true}
	}
	return result, nil
}

func (c *Client) ReleaseLease(ctx context.Context, lease uuid.UUID) error {
	if lease == uuid.Nil {
		return invalidRequest()
	}
	status, data, failure := c.exchange(ctx, http.MethodDelete, "/v1/ui-lease", lease, nil)
	if failure != nil {
		return failure
	}
	if status != http.StatusNoContent || len(data) != 0 {
		return &RequestError{Status: status, Code: "invalid_response", OutcomeUnknown: true}
	}
	return nil
}

func (c *Client) Snapshot(ctx context.Context, lease uuid.UUID) (contract.Snapshot, error) {
	var result contract.Snapshot
	err := c.get(ctx, "/v1/snapshot", lease, "Snapshot", &result)
	return result, err
}

func (c *Client) ListTests(ctx context.Context, lease uuid.UUID, params contract.ListTestsParams) (contract.TestList, error) {
	var result contract.TestList
	query := url.Values{}
	if params.After != nil {
		if *params.After == uuid.Nil {
			return result, invalidRequest()
		}
		query.Set("after", params.After.String())
	}
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > 500 {
			return result, invalidRequest()
		}
		query.Set("limit", strconv.Itoa(*params.Limit))
	}
	err := c.get(ctx, "/v1/tests?"+query.Encode(), lease, "TestList", &result)
	if err == nil {
		limit := 100
		if params.Limit != nil {
			limit = *params.Limit
		}
		previous := ""
		if params.After != nil {
			previous = params.After.String()
		}
		if len(result.Items) > limit {
			return contract.TestList{}, &RequestError{Code: "invalid_response"}
		}
		for _, item := range result.Items {
			if item.TestId == uuid.Nil || item.StopCommandId == uuid.Nil || strings.Compare(item.TestId.String(), previous) <= 0 {
				return contract.TestList{}, &RequestError{Code: "invalid_response"}
			}
			previous = item.TestId.String()
		}
		if result.NextAfter != nil && (len(result.Items) == 0 || result.NextAfter.String() != previous) {
			return contract.TestList{}, &RequestError{Code: "invalid_response"}
		}
	}
	return result, err
}

func (c *Client) Test(ctx context.Context, lease, id uuid.UUID) (contract.TestRecord, error) {
	var result contract.TestRecord
	if id == uuid.Nil {
		return result, invalidRequest()
	}
	err := c.get(ctx, "/v1/tests/"+id.String(), lease, "TestRecord", &result)
	if err == nil && (result.TestId != id || result.StopCommandId == uuid.Nil) {
		return contract.TestRecord{}, &RequestError{Code: "invalid_response"}
	}
	return result, err
}

func (c *Client) Readiness(ctx context.Context, lease, id uuid.UUID) (contract.Readiness, error) {
	var result contract.Readiness
	if id == uuid.Nil {
		return result, invalidRequest()
	}
	err := c.get(ctx, "/v1/tests/"+id.String()+"/readiness", lease, "Readiness", &result)
	if err == nil && result.TestId != id {
		return contract.Readiness{}, &RequestError{Code: "invalid_response"}
	}
	return result, err
}

func pageQuery(after int64, limit int) (string, error) {
	if after < 0 || limit < 1 || limit > 500 {
		return "", invalidRequest()
	}
	query := url.Values{"after": {strconv.FormatInt(after, 10)}, "limit": {strconv.Itoa(limit)}}
	return "?" + query.Encode(), nil
}

func (c *Client) Measurements(ctx context.Context, lease, id uuid.UUID, after int64, limit int) (contract.MeasurementPage, error) {
	var result contract.MeasurementPage
	query, err := pageQuery(after, limit)
	if err != nil || id == uuid.Nil {
		return result, invalidRequest()
	}
	err = c.get(ctx, "/v1/tests/"+id.String()+"/measurements"+query, lease, "MeasurementPage", &result)
	if err == nil {
		previous := after
		if len(result.Items) > limit {
			return contract.MeasurementPage{}, &RequestError{Code: "invalid_response"}
		}
		for _, item := range result.Items {
			if item.Sequence <= previous {
				return contract.MeasurementPage{}, &RequestError{Code: "invalid_response"}
			}
			previous = item.Sequence
		}
		if result.NextAfter != nil && (len(result.Items) == 0 || *result.NextAfter != previous) {
			return contract.MeasurementPage{}, &RequestError{Code: "invalid_response"}
		}
	}
	return result, err
}

func (c *Client) Audit(ctx context.Context, lease, id uuid.UUID, after int64, limit int) (contract.AuditPage, error) {
	var result contract.AuditPage
	query, err := pageQuery(after, limit)
	if err != nil || id == uuid.Nil {
		return result, invalidRequest()
	}
	err = c.get(ctx, "/v1/tests/"+id.String()+"/audit"+query, lease, "AuditPage", &result)
	if err == nil {
		previous := after
		if len(result.Items) > limit {
			return contract.AuditPage{}, &RequestError{Code: "invalid_response"}
		}
		for _, item := range result.Items {
			if item.TestId != id || item.Sequence <= previous {
				return contract.AuditPage{}, &RequestError{Code: "invalid_response"}
			}
			previous = item.Sequence
		}
		if result.NextAfter != nil && (len(result.Items) == 0 || *result.NextAfter != previous) {
			return contract.AuditPage{}, &RequestError{Code: "invalid_response"}
		}
	}
	return result, err
}

func (c *Client) Command(ctx context.Context, lease, id uuid.UUID) (contract.CommandStatus, error) {
	var result contract.CommandStatus
	if id == uuid.Nil {
		return result, invalidRequest()
	}
	err := c.get(ctx, "/v1/commands/"+id.String(), lease, "CommandStatus", &result)
	if err == nil && result.CommandId != id {
		return contract.CommandStatus{}, &RequestError{Code: "invalid_response"}
	}
	return result, err
}

func (c *Client) command(ctx context.Context, lease uuid.UUID, path string, id uuid.UUID, payload any) (contract.CommandStatus, error) {
	var result contract.CommandStatus
	if lease == uuid.Nil || id == uuid.Nil {
		return result, invalidRequest()
	}
	status, data, failure := c.exchange(ctx, http.MethodPost, path, lease, payload)
	if failure != nil {
		return result, failure
	}
	valid := false
	switch status {
	case http.StatusOK:
		valid = validateResponse("CommandStatus", data, &result) == nil
	case http.StatusAccepted:
		var accepted contract.CommandAccepted
		valid = validateResponse("CommandAccepted", data, &accepted) == nil
		result = contract.CommandStatus{CommandId: accepted.CommandId, Status: contract.CommandStatusStatus(accepted.Status), TestId: accepted.TestId, Reason: accepted.Reason}
	}
	if !valid || result.CommandId != id {
		return contract.CommandStatus{}, &RequestError{Status: status, Code: "invalid_response", OutcomeUnknown: true}
	}
	return result, nil
}

func (c *Client) CreateTest(ctx context.Context, lease uuid.UUID, command contract.CreateTestCommand) (contract.CommandStatus, error) {
	return c.command(ctx, lease, "/v1/tests", command.CommandId, command)
}

// CreateTestRaw forwards an already inspected original command to the fixed create route.
func (c *Client) CreateTestRaw(ctx context.Context, lease, id uuid.UUID, body []byte) (contract.CommandStatus, error) {
	return c.command(ctx, lease, "/v1/tests", id, json.RawMessage(body))
}

func (c *Client) StartTest(ctx context.Context, lease, testID uuid.UUID, command contract.StartTestCommand) (contract.CommandStatus, error) {
	if testID == uuid.Nil {
		return contract.CommandStatus{}, invalidRequest()
	}
	result, err := c.command(ctx, lease, "/v1/tests/"+testID.String()+"/start", command.CommandId, command)
	return matchCommandTest(result, err, testID)
}

func (c *Client) StartTestRaw(ctx context.Context, lease, testID, id uuid.UUID, body []byte) (contract.CommandStatus, error) {
	if testID == uuid.Nil {
		return contract.CommandStatus{}, invalidRequest()
	}
	result, err := c.command(ctx, lease, "/v1/tests/"+testID.String()+"/start", id, json.RawMessage(body))
	return matchCommandTest(result, err, testID)
}

func (c *Client) ConfirmAction(ctx context.Context, lease, testID, actionID uuid.UUID, command contract.ConfirmActionCommand) (contract.CommandStatus, error) {
	if testID == uuid.Nil || actionID == uuid.Nil {
		return contract.CommandStatus{}, invalidRequest()
	}
	result, err := c.command(ctx, lease, "/v1/tests/"+testID.String()+"/actions/"+actionID.String()+"/confirm", command.CommandId, command)
	return matchCommandTest(result, err, testID)
}

func (c *Client) ConfirmActionRaw(ctx context.Context, lease, testID, actionID, id uuid.UUID, body []byte) (contract.CommandStatus, error) {
	if testID == uuid.Nil || actionID == uuid.Nil {
		return contract.CommandStatus{}, invalidRequest()
	}
	result, err := c.command(ctx, lease, "/v1/tests/"+testID.String()+"/actions/"+actionID.String()+"/confirm", id, json.RawMessage(body))
	return matchCommandTest(result, err, testID)
}

func (c *Client) StopTest(ctx context.Context, lease, testID uuid.UUID, command contract.StopTestCommand) (contract.CommandStatus, error) {
	if testID == uuid.Nil {
		return contract.CommandStatus{}, invalidRequest()
	}
	result, err := c.command(ctx, lease, "/v1/tests/"+testID.String()+"/stop", command.CommandId, command)
	return matchCommandTest(result, err, testID)
}

func (c *Client) StopTestRaw(ctx context.Context, lease, testID, id uuid.UUID, body []byte) (contract.CommandStatus, error) {
	if testID == uuid.Nil {
		return contract.CommandStatus{}, invalidRequest()
	}
	result, err := c.command(ctx, lease, "/v1/tests/"+testID.String()+"/stop", id, json.RawMessage(body))
	return matchCommandTest(result, err, testID)
}

func matchCommandTest(result contract.CommandStatus, err error, testID uuid.UUID) (contract.CommandStatus, error) {
	if err == nil && result.TestId != nil && *result.TestId != testID {
		return contract.CommandStatus{}, &RequestError{Code: "invalid_response", OutcomeUnknown: true}
	}
	return result, err
}
