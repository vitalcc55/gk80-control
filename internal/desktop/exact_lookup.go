package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/transport"
)

// LookupCommandExact checks the saved attempt without sending the command again.
func (o *Owner) LookupCommandExact(ctx context.Context, databaseValue, kind, testValue, actionValue, commandValue, originalJSON string) (contract.ExactLookupResult, error) {
	var empty contract.ExactLookupResult
	database, err := parseSessionID(databaseValue)
	if err != nil || !strings.EqualFold(database.String(), databaseValue) || len(originalJSON) > 8192 {
		return empty, errSessionUnavailable
	}
	commandID, err := parseSessionID(commandValue)
	if err != nil || !strings.EqualFold(commandID.String(), commandValue) {
		return empty, errSessionUnavailable
	}
	var testID, actionID uuid.UUID
	if testValue != "" {
		testID, err = parseSessionID(testValue)
		if err != nil || !strings.EqualFold(testID.String(), testValue) {
			return empty, errSessionUnavailable
		}
	}
	if actionValue != "" {
		actionID, err = parseSessionID(actionValue)
		if err != nil || !strings.EqualFold(actionID.String(), actionValue) {
			return empty, errSessionUnavailable
		}
	}
	var raw []byte
	var identity string
	switch kind {
	case "create":
		var input CreateCommandInput
		if testID != uuid.Nil || actionID != uuid.Nil || json.Unmarshal([]byte(originalJSON), &input) != nil || input.CommandId != commandID {
			return empty, errSessionUnavailable
		}
		raw = input.body
		identity = "create:" + string(input.identity)
	case "start":
		var input StartCommandInput
		if testID == uuid.Nil || actionID != uuid.Nil || json.Unmarshal([]byte(originalJSON), &input) != nil || input.CommandId != commandID {
			return empty, errSessionUnavailable
		}
		raw = input.body
		identity = "start:" + testID.String() + ":" + string(input.identity)
	case "confirm":
		var input ConfirmCommandInput
		if testID == uuid.Nil || actionID == uuid.Nil || json.Unmarshal([]byte(originalJSON), &input) != nil || input.CommandId != commandID {
			return empty, errSessionUnavailable
		}
		raw = input.body
		identity = "confirm:" + testID.String() + ":" + actionID.String() + ":" + string(input.identity)
	default:
		return empty, errSessionUnavailable
	}
	if ctx.Err() != nil {
		return empty, ctx.Err()
	}
	o.mu.Lock()
	session, process := o.session, o.process
	valid := o.status.Status == contract.DesktopStatusStatusReady && session != nil && process != nil && o.backendID == session.backendID
	if valid {
		select {
		case <-process.Done():
			valid = false
		default:
		}
	}
	if !valid {
		o.mu.Unlock()
		return empty, errSessionUnavailable
	}
	session.mu.Lock()
	lease := session.leaseID
	valid = !session.closed && !session.closing && session.leaseConfirmed && lease != uuid.Nil && time.Now().Before(session.leaseUntil)
	session.mu.Unlock()
	if valid && ctx.Err() == nil && o.databaseID != database {
		o.mu.Unlock()
		return contract.ExactLookupResult{Outcome: contract.ExactLookupResultOutcomeDatabaseMismatch}, nil
	}
	o.mu.Unlock()
	if !valid || ctx.Err() != nil {
		return empty, errSessionUnavailable
	}
	readCtx, cancel := context.WithTimeout(ctx, sessionRequestLimit)
	defer cancel()
	stop := context.AfterFunc(session.ctx, cancel)
	defer stop()
	var status contract.CommandStatus
	switch kind {
	case "create":
		status, err = session.client.LookupCreateExact(readCtx, lease, database, commandID, raw)
	case "start":
		status, err = session.client.LookupStartExact(readCtx, lease, database, testID, commandID, raw)
	case "confirm":
		status, err = session.client.LookupConfirmExact(readCtx, lease, database, testID, actionID, commandID, raw)
	}
	if readCtx.Err() != nil {
		return empty, readCtx.Err()
	}
	var failure *transport.RequestError
	conflict := errors.As(err, &failure) && !failure.OutcomeUnknown && failure.Status == http.StatusConflict && failure.Code == "command_conflict"
	o.mu.Lock()
	valid = o.session == session && o.process == process && o.databaseID == database && o.backendID == session.backendID && o.status.Status == contract.DesktopStatusStatusReady
	if valid {
		select {
		case <-process.Done():
			valid = false
		default:
		}
	}
	session.mu.Lock()
	valid = valid && !session.closed && !session.closing && session.leaseConfirmed && session.leaseID == lease && time.Now().Before(session.leaseUntil)
	busy := false
	if current, ok := session.commands[commandID]; valid && readCtx.Err() == nil && ok && session.commandPayloads[commandID] == identity {
		if session.inFlight[commandID] {
			busy = true
		} else if conflict {
			delete(session.commands, commandID)
			delete(session.commandPayloads, commandID)
			delete(session.ambiguous, commandID)
		} else if err == nil && status.Status != contract.CommandStatusStatusUnknown {
			session.commands[commandID] = mergeCommandStatus(current, status)
			delete(session.ambiguous, commandID)
		}
	}
	session.mu.Unlock()
	o.mu.Unlock()
	if !valid || readCtx.Err() != nil {
		return empty, errSessionUnavailable
	}
	if busy {
		return empty, errSessionBusy
	}
	if errors.As(err, &failure) && !failure.OutcomeUnknown && failure.Status == http.StatusConflict {
		switch failure.Code {
		case "command_conflict":
			return contract.ExactLookupResult{Outcome: contract.ExactLookupResultOutcomeConflict}, nil
		case "database_mismatch":
			return contract.ExactLookupResult{Outcome: contract.ExactLookupResultOutcomeDatabaseMismatch}, nil
		}
	}
	if err != nil {
		return empty, err
	}
	if status.Status == contract.CommandStatusStatusUnknown {
		return contract.ExactLookupResult{Outcome: contract.ExactLookupResultOutcomeUnknown}, nil
	}
	return contract.ExactLookupResult{Outcome: contract.ExactLookupResultOutcomeMatch, Command: &status}, nil
}
