package desktop

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
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

func (o *Owner) StopTest(ctx context.Context, value string, command contract.StopTestCommand) (contract.CommandStatus, error) {
	id, err := parseSessionID(value)
	if err != nil {
		return contract.CommandStatus{}, err
	}
	session, err := o.currentSession()
	if err != nil {
		return contract.CommandStatus{}, err
	}
	identity, err := commandIdentity("stop:"+id.String(), command)
	if err != nil {
		return contract.CommandStatus{}, err
	}
	session.mu.Lock()
	var active *contract.TestRecord
	if session.snapshot != nil {
		active = session.snapshot.ActiveTest
	}
	valid := (active != nil && active.Stage != "finished" && active.TestId == id && active.StopCommandId == command.CommandId) || (session.activeTestID == id && session.activeStopID == command.CommandId)
	previous, tracked := session.commands[command.CommandId]
	knownRepeat := tracked && session.commandPayloads[command.CommandId] == identity
	session.mu.Unlock()
	if !valid {
		if knownRepeat {
			return previous, nil
		}
		return contract.CommandStatus{}, errSessionUnavailable
	}
	return session.command(ctx, command.CommandId, identity, true, func(ctx context.Context, lease uuid.UUID) (contract.CommandStatus, error) {
		return session.client.StopTest(ctx, lease, id, command)
	})
}

var _ Core = (*Owner)(nil)
