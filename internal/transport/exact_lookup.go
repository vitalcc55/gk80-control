package transport

import (
	"context"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

var exactInputValidation struct {
	once      sync.Once
	validator *contract.InputValidator
	err       error
}

// exactLookup sends only a read-only lookup. Its errors do not resolve the original command.
func (c *Client) exactLookup(ctx context.Context, lease, database, id, testID uuid.UUID, path, schema string, body []byte) (contract.CommandStatus, error) {
	var result contract.CommandStatus
	if lease == uuid.Nil || database == uuid.Nil || id == uuid.Nil || len(body) > 8192 {
		return result, invalidRequest()
	}
	body = append([]byte(nil), body...)
	exactInputValidation.once.Do(func() { exactInputValidation.validator, exactInputValidation.err = contract.NewInputValidator() })
	if exactInputValidation.err != nil {
		return result, invalidRequest()
	}
	var received uuid.UUID
	switch schema {
	case "CreateTestCommand":
		var input contract.CreateTestCommand
		if _, err := exactInputValidation.validator.Inspect(schema, body, &input); err != nil {
			return result, invalidRequest()
		}
		received = input.CommandId
	case "StartTestCommand":
		var input contract.StartTestCommand
		if _, err := exactInputValidation.validator.Inspect(schema, body, &input); err != nil {
			return result, invalidRequest()
		}
		received = input.CommandId
	case "ConfirmActionCommand":
		var input contract.ConfirmActionCommand
		if _, err := exactInputValidation.validator.Inspect(schema, body, &input); err != nil {
			return result, invalidRequest()
		}
		received = input.CommandId
	case "StopTestCommand":
		var input contract.StopTestCommand
		if _, err := exactInputValidation.validator.Inspect(schema, body, &input); err != nil {
			return result, invalidRequest()
		}
		received = input.CommandId
	default:
		return result, invalidRequest()
	}
	if received != id {
		return result, invalidRequest()
	}
	status, data, failure := c.exchangeBytes(ctx, http.MethodPost, path, lease, database, body, true, false)
	if failure != nil {
		return result, failure
	}
	// A known non-rejected command must identify its test; rejected and unknown
	// results can legitimately precede the existence of that test.
	if status != http.StatusOK || validateResponse("CommandStatus", data, &result) != nil || result.CommandId != id ||
		(result.TestId != nil && (*result.TestId == uuid.Nil || (testID != uuid.Nil && *result.TestId != testID))) ||
		(result.TestId == nil && result.Status != contract.CommandStatusStatusUnknown && (schema == "StopTestCommand" || result.Status != contract.CommandStatusStatusRejected)) {
		return contract.CommandStatus{}, &RequestError{Status: status, Code: "invalid_response"}
	}
	return result, nil
}

func (c *Client) LookupCreateExact(ctx context.Context, lease, database, id uuid.UUID, body []byte) (contract.CommandStatus, error) {
	return c.exactLookup(ctx, lease, database, id, uuid.Nil, "/v1/commands/lookup/create", "CreateTestCommand", body)
}

func (c *Client) LookupStartExact(ctx context.Context, lease, database, testID, id uuid.UUID, body []byte) (contract.CommandStatus, error) {
	if testID == uuid.Nil {
		return contract.CommandStatus{}, invalidRequest()
	}
	return c.exactLookup(ctx, lease, database, id, testID, "/v1/commands/lookup/start/"+testID.String(), "StartTestCommand", body)
}

func (c *Client) LookupConfirmExact(ctx context.Context, lease, database, testID, actionID, id uuid.UUID, body []byte) (contract.CommandStatus, error) {
	if testID == uuid.Nil || actionID == uuid.Nil {
		return contract.CommandStatus{}, invalidRequest()
	}
	return c.exactLookup(ctx, lease, database, id, testID, "/v1/commands/lookup/confirm/"+testID.String()+"/"+actionID.String(), "ConfirmActionCommand", body)
}

func (c *Client) LookupStopExact(ctx context.Context, lease, database, testID, id uuid.UUID, body []byte) (contract.CommandStatus, error) {
	if testID == uuid.Nil {
		return contract.CommandStatus{}, invalidRequest()
	}
	return c.exactLookup(ctx, lease, database, id, testID, "/v1/commands/lookup/stop/"+testID.String(), "StopTestCommand", body)
}
