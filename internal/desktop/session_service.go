package desktop

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/diagnostics"
	"github.com/vitalcc55/gk80-control/internal/transport"
)

func (o *Owner) currentSession() (*demoSession, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.session == nil || o.status.Status == contract.DesktopStatusStatusClosing || o.status.Status == contract.DesktopStatusStatusExited {
		return nil, errSessionUnavailable
	}
	return o.session, nil
}

func (o *Owner) Handshake(context.Context) (contract.Handshake, error) {
	status := o.Status()
	if status.Handshake == nil || status.Status != contract.DesktopStatusStatusReady {
		return contract.Handshake{}, errSessionUnavailable
	}
	return *status.Handshake, nil
}

func (o *Owner) KeepAlive(ctx context.Context) error {
	session, err := o.currentSession()
	if err != nil {
		return err
	}
	return session.keepAlive(ctx)
}

func (o *Owner) ConnectionState(context.Context) (contract.ConnectionState, error) {
	session, err := o.currentSession()
	if err != nil {
		return contract.ConnectionState{}, err
	}
	return session.connectionState(), nil
}

func (o *Owner) SessionView(context.Context) (contract.SessionView, error) {
	session, err := o.currentSession()
	if err != nil {
		return contract.SessionView{}, err
	}
	view, err := session.sessionView()
	if err != nil {
		return contract.SessionView{}, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.session != session || o.status.Status != contract.DesktopStatusStatusReady || o.process == nil {
		return contract.SessionView{}, errSessionUnavailable
	}
	select {
	case <-o.process.Done():
		return contract.SessionView{}, errSessionUnavailable
	default:
		return view, nil
	}
}

func (o *Owner) Snapshot(context.Context) (contract.Snapshot, error) {
	session, err := o.currentSession()
	if err != nil {
		return contract.Snapshot{}, err
	}
	return session.snapshotCopy()
}

func (o *Owner) ListTests(ctx context.Context, params contract.ListTestsParams) (contract.TestList, error) {
	session, err := o.currentSession()
	if err != nil {
		return contract.TestList{}, err
	}
	lease, err := session.currentLease()
	if err != nil {
		return contract.TestList{}, err
	}
	return session.client.ListTests(ctx, lease, params)
}

func parseSessionID(value string) (uuid.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, errors.New("invalid identifier")
	}
	return id, nil
}

func (o *Owner) Test(ctx context.Context, value string) (contract.TestRecord, error) {
	id, err := parseSessionID(value)
	if err != nil {
		return contract.TestRecord{}, err
	}
	session, err := o.currentSession()
	if err != nil {
		return contract.TestRecord{}, err
	}
	lease, err := session.currentLease()
	if err != nil {
		return contract.TestRecord{}, err
	}
	session.mu.Lock()
	generation := session.activeGeneration
	session.mu.Unlock()
	record, err := session.client.Test(ctx, lease, id)
	if err == nil {
		session.mu.Lock()
		finishedInSnapshot := session.snapshot != nil && session.snapshot.ActiveTest != nil && session.snapshot.ActiveTest.TestId == record.TestId && session.snapshot.ActiveTest.Stage == "finished"
		differentActive := session.snapshot != nil && session.snapshot.ActiveTest != nil && session.snapshot.ActiveTest.TestId != record.TestId && session.snapshot.ActiveTest.Stage != "finished"
		if !finishedInSnapshot && !differentActive && (session.activeGeneration == generation || session.activeTestID == record.TestId) {
			session.rememberStopLocked(record)
		} else {
			session.rememberStopPairLocked(record)
		}
		session.mu.Unlock()
	}
	return record, err
}

func (o *Owner) Readiness(ctx context.Context, value string) (contract.Readiness, error) {
	id, err := parseSessionID(value)
	if err != nil {
		return contract.Readiness{}, err
	}
	session, err := o.currentSession()
	if err != nil {
		return contract.Readiness{}, err
	}
	lease, err := session.currentLease()
	if err != nil {
		return contract.Readiness{}, err
	}
	return session.client.Readiness(ctx, lease, id)
}

func (o *Owner) Measurements(ctx context.Context, value string, after int64, limit int) (contract.MeasurementPage, error) {
	id, err := parseSessionID(value)
	if err != nil {
		return contract.MeasurementPage{}, err
	}
	session, err := o.currentSession()
	if err != nil {
		return contract.MeasurementPage{}, err
	}
	lease, err := session.currentLease()
	if err != nil {
		return contract.MeasurementPage{}, err
	}
	return session.client.Measurements(ctx, lease, id, after, limit)
}

func (o *Owner) Audit(ctx context.Context, value string, after int64, limit int) (contract.AuditPage, error) {
	id, err := parseSessionID(value)
	if err != nil {
		return contract.AuditPage{}, err
	}
	session, err := o.currentSession()
	if err != nil {
		return contract.AuditPage{}, err
	}
	lease, err := session.currentLease()
	if err != nil {
		return contract.AuditPage{}, err
	}
	return session.client.Audit(ctx, lease, id, after, limit)
}

func (o *Owner) Command(ctx context.Context, value string) (contract.CommandStatus, error) {
	id, err := parseSessionID(value)
	if err != nil {
		return contract.CommandStatus{}, err
	}
	session, err := o.currentSession()
	if err != nil {
		return contract.CommandStatus{}, err
	}
	lease, err := session.currentLease()
	if err != nil {
		return contract.CommandStatus{}, err
	}
	session.mu.Lock()
	if current, tracked := session.commands[id]; tracked && (session.inFlight[id] || session.ambiguous[id]) {
		session.mu.Unlock()
		return current, nil
	}
	session.mu.Unlock()
	status, err := session.client.Command(ctx, lease, id)
	if err != nil {
		return contract.CommandStatus{}, err
	}
	session.mu.Lock()
	if current, tracked := session.commands[id]; tracked {
		if session.inFlight[id] || session.ambiguous[id] {
			status = current
		} else {
			status = mergeCommandStatus(current, status)
			session.commands[id] = status
		}
	}
	session.mu.Unlock()
	return status, nil
}

func commandIdentity(operation string, command any) (string, error) {
	data, err := json.Marshal(command)
	if err != nil {
		return "", errSessionUnavailable
	}
	return operation + ":" + string(data), nil
}

func (o *Owner) forwardCommand(ctx context.Context, session *demoSession, commandID, testID, actionID uuid.UUID, send func(context.Context) (contract.CommandStatus, error)) (contract.CommandStatus, error) {
	args := []any{"command_id", commandID, "backend_run_id", session.backendID}
	if testID != uuid.Nil {
		args = append(args, "test_id", testID)
	}
	if actionID != uuid.Nil {
		args = append(args, "action_id", actionID)
	}
	if o.log != nil {
		o.log.Info(string(diagnostics.CommandSent), args...)
	}
	result, err := send(ctx)
	if o.log == nil {
		return result, err
	}
	if result.TestId != nil && testID == uuid.Nil {
		args = append(args, "test_id", *result.TestId)
	}
	if err == nil && result.Status.Valid() {
		args = append(args, "command_status", string(result.Status))
		if result.Reason != nil {
			args = append(args, "error_code", *result.Reason)
		}
		o.log.Info(string(diagnostics.CommandObserved), args...)
	} else {
		var failure *transport.RequestError
		if errors.As(err, &failure) && !failure.OutcomeUnknown {
			switch failure.Code {
			case "command_conflict":
				args = append(args, "error_code", "command_id_conflict")
			case "invalid_request":
				args = append(args, "error_code", "invalid_command")
			case "not_found":
				args = append(args, "error_code", "not_found")
			case "busy", "invalid_state":
				args = append(args, "error_code", failure.Code)
			}
			o.log.Info(string(diagnostics.CommandRejected), args...)
		} else {
			args = append(args, "command_status", "unknown")
			if errors.As(err, &failure) {
				switch failure.Code {
				case "unavailable", "invalid_response", "lease_invalid", "state_unavailable", "outcome_unknown":
					args = append(args, "error_code", failure.Code)
				}
			}
			o.log.Info(string(diagnostics.CommandObserved), args...)
		}
	}
	return result, err
}

func (o *Owner) CreateTest(ctx context.Context, command contract.CreateTestCommand) (contract.CommandStatus, error) {
	session, err := o.currentSession()
	if err != nil {
		return contract.CommandStatus{}, err
	}
	identity, err := commandIdentity("create", command)
	if err != nil {
		return contract.CommandStatus{}, err
	}
	return session.command(ctx, command.CommandId, identity, false, func(ctx context.Context, lease uuid.UUID) (contract.CommandStatus, error) {
		return session.client.CreateTest(ctx, lease, command)
	})
}

func (o *Owner) CreateTestInput(ctx context.Context, command CreateCommandInput) (contract.CommandStatus, error) {
	if len(command.body) == 0 || len(command.identity) == 0 {
		return contract.CommandStatus{}, errSessionUnavailable
	}
	session, err := o.currentSession()
	if err != nil {
		return contract.CommandStatus{}, err
	}
	var invalidNew error
	if !command.lossless {
		invalidNew = errors.New("decimal value cannot be preserved")
	}
	return session.command(ctx, command.CommandId, "create:"+string(command.identity), false, func(ctx context.Context, lease uuid.UUID) (contract.CommandStatus, error) {
		return o.forwardCommand(ctx, session, command.CommandId, uuid.Nil, uuid.Nil, func(ctx context.Context) (contract.CommandStatus, error) {
			return session.client.CreateTestRaw(ctx, lease, command.CommandId, command.body)
		})
	}, invalidNew)
}

func (o *Owner) StartTest(ctx context.Context, value string, command contract.StartTestCommand) (contract.CommandStatus, error) {
	id, err := parseSessionID(value)
	if err != nil {
		return contract.CommandStatus{}, err
	}
	session, err := o.currentSession()
	if err != nil {
		return contract.CommandStatus{}, err
	}
	identity, err := commandIdentity("start:"+id.String(), command)
	if err != nil {
		return contract.CommandStatus{}, err
	}
	return session.command(ctx, command.CommandId, identity, false, func(ctx context.Context, lease uuid.UUID) (contract.CommandStatus, error) {
		return session.client.StartTest(ctx, lease, id, command)
	})
}

func (o *Owner) StartTestInput(ctx context.Context, value string, command StartCommandInput) (contract.CommandStatus, error) {
	id, err := parseSessionID(value)
	if err != nil || len(command.body) == 0 || len(command.identity) == 0 {
		return contract.CommandStatus{}, errSessionUnavailable
	}
	session, err := o.currentSession()
	if err != nil {
		return contract.CommandStatus{}, err
	}
	identity := "start:" + id.String() + ":" + string(command.identity)
	return session.command(ctx, command.CommandId, identity, false, func(ctx context.Context, lease uuid.UUID) (contract.CommandStatus, error) {
		return o.forwardCommand(ctx, session, command.CommandId, id, uuid.Nil, func(ctx context.Context) (contract.CommandStatus, error) {
			return session.client.StartTestRaw(ctx, lease, id, command.CommandId, command.body)
		})
	})
}

func (o *Owner) ConfirmAction(ctx context.Context, value, actionValue string, command contract.ConfirmActionCommand) (contract.CommandStatus, error) {
	id, err := parseSessionID(value)
	if err != nil {
		return contract.CommandStatus{}, err
	}
	action, err := parseSessionID(actionValue)
	if err != nil {
		return contract.CommandStatus{}, err
	}
	session, err := o.currentSession()
	if err != nil {
		return contract.CommandStatus{}, err
	}
	identity, err := commandIdentity("confirm:"+id.String()+":"+action.String(), command)
	if err != nil {
		return contract.CommandStatus{}, err
	}
	return session.command(ctx, command.CommandId, identity, false, func(ctx context.Context, lease uuid.UUID) (contract.CommandStatus, error) {
		return session.client.ConfirmAction(ctx, lease, id, action, command)
	})
}

func (o *Owner) ConfirmActionInput(ctx context.Context, value, actionValue string, command ConfirmCommandInput) (contract.CommandStatus, error) {
	id, err := parseSessionID(value)
	if err != nil {
		return contract.CommandStatus{}, err
	}
	action, err := parseSessionID(actionValue)
	if err != nil || len(command.body) == 0 || len(command.identity) == 0 {
		return contract.CommandStatus{}, errSessionUnavailable
	}
	session, err := o.currentSession()
	if err != nil {
		return contract.CommandStatus{}, err
	}
	identity := "confirm:" + id.String() + ":" + action.String() + ":" + string(command.identity)
	return session.command(ctx, command.CommandId, identity, false, func(ctx context.Context, lease uuid.UUID) (contract.CommandStatus, error) {
		return o.forwardCommand(ctx, session, command.CommandId, id, action, func(ctx context.Context) (contract.CommandStatus, error) {
			return session.client.ConfirmActionRaw(ctx, lease, id, action, command.CommandId, command.body)
		})
	})
}

func (o *Owner) StopTest(ctx context.Context, value string, command contract.StopTestCommand) (contract.CommandStatus, error) {
	id, err := parseSessionID(value)
	if err != nil {
		return contract.CommandStatus{}, err
	}
	identity, err := commandIdentity("stop:"+id.String(), command)
	if err != nil {
		return contract.CommandStatus{}, err
	}
	return o.stopSessionCommand(ctx, id, command.CommandId, identity, func(ctx context.Context, _ *demoSession, client sessionClient, lease uuid.UUID) (contract.CommandStatus, error) {
		return client.StopTest(ctx, lease, id, command)
	})
}

func (o *Owner) StopTestInput(ctx context.Context, value string, command StopCommandInput) (contract.CommandStatus, error) {
	id, err := parseSessionID(value)
	if err != nil || len(command.body) == 0 || len(command.identity) == 0 {
		return contract.CommandStatus{}, errSessionUnavailable
	}
	identity := "stop:" + id.String() + ":" + string(command.identity)
	return o.stopSessionCommand(ctx, id, command.CommandId, identity, func(ctx context.Context, session *demoSession, client sessionClient, lease uuid.UUID) (contract.CommandStatus, error) {
		return o.forwardCommand(ctx, session, command.CommandId, id, uuid.Nil, func(ctx context.Context) (contract.CommandStatus, error) {
			return client.StopTestRaw(ctx, lease, id, command.CommandId, command.body)
		})
	})
}

func (o *Owner) stopSessionCommand(ctx context.Context, id, commandID uuid.UUID, identity string, send func(context.Context, *demoSession, sessionClient, uuid.UUID) (contract.CommandStatus, error)) (contract.CommandStatus, error) {
	session, err := o.currentSession()
	if err != nil {
		return contract.CommandStatus{}, err
	}
	session.mu.Lock()
	var active *contract.TestRecord
	if session.snapshot != nil {
		active = session.snapshot.ActiveTest
	}
	valid := (active != nil && active.Stage != "finished" && active.TestId == id && active.StopCommandId == commandID) || (session.activeTestID == id && session.activeStopID == commandID)
	previous, tracked := session.commands[commandID]
	knownRepeat := tracked && session.commandPayloads[commandID] == identity
	session.mu.Unlock()
	if !valid {
		if knownRepeat {
			return previous, nil
		}
		return contract.CommandStatus{}, errSessionUnavailable
	}
	return session.command(ctx, commandID, identity, true, func(ctx context.Context, lease uuid.UUID) (contract.CommandStatus, error) {
		return send(ctx, session, session.client, lease)
	})
}

var _ Core = (*Owner)(nil)
