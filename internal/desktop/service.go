// Package desktop defines the subject-specific boundary exposed by Wails.
package desktop

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/transport"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// ErrCommandConflict means that the identifier belongs to different command content.
var ErrCommandConflict = errors.New("command identifier already used with different content")

// Core is implemented by the authenticated native client, not by the frontend.
type Core interface {
	Handshake(context.Context) (contract.Handshake, error)
	ConnectionState(context.Context) (contract.ConnectionState, error)
	SessionView(context.Context) (contract.SessionView, error)
	EnableConnectionEvents(context.Context) error
	AcknowledgeConnection(context.Context, string, int64) error
	AcknowledgeCoreEvent(context.Context, string, int64) error
	WaitConnection(context.Context, int64, int64) (contract.ConnectionNotice, error)
	KeepAliveUntil(context.Context, time.Time) error
	Snapshot(context.Context) (contract.Snapshot, error)
	ListTests(context.Context, contract.ListTestsParams) (contract.TestList, error)
	CreateTestInput(context.Context, CreateCommandInput) (contract.CommandStatus, error)
	Test(context.Context, string) (contract.TestRecord, error)
	Readiness(context.Context, string) (contract.Readiness, error)
	StartTestInput(context.Context, string, StartCommandInput) (contract.CommandStatus, error)
	StopTestInput(context.Context, string, StopCommandInput) (contract.CommandStatus, error)
	ConfirmActionInput(context.Context, string, string, ConfirmCommandInput) (contract.CommandStatus, error)
	Measurements(context.Context, string, int64, int) (contract.MeasurementPage, error)
	Audit(context.Context, string, int64, int) (contract.AuditPage, error)
	Command(context.Context, string) (contract.CommandStatus, error)
	LookupCommandExact(context.Context, string, string, string, string, string, string) (contract.ExactLookupResult, error)
}

// Service forwards domain operations to the authenticated core client.
type Service struct {
	core            Core
	sessionGate     sync.Mutex
	observationGate sync.Mutex
	watchGate       sync.Mutex
	lookupGate      sync.Mutex
	ticketMu        sync.Mutex
	ticket          uuid.UUID
	ticketUntil     time.Time
}

func NewService(core Core) *Service { return &Service{core: core} }

// NewWailsService registers the domain service for the desktop application.
func NewWailsService(core Core) application.Service {
	return application.NewService(NewService(core))
}

func (s *Service) Handshake(ctx context.Context) (contract.Handshake, error) {
	return s.core.Handshake(ctx)
}

func (s *Service) ConnectionState(ctx context.Context) (contract.ConnectionState, error) {
	return s.core.ConnectionState(ctx)
}

func (s *Service) SessionView(ctx context.Context) (contract.SessionView, error) {
	if !s.sessionGate.TryLock() {
		return contract.SessionView{}, errSessionBusy
	}
	defer s.sessionGate.Unlock()
	return s.core.SessionView(ctx)
}

// ObserveSession reads the last host view without extending UI liveness.
func (s *Service) ObserveSession(ctx context.Context) (contract.SessionView, error) {
	if !s.observationGate.TryLock() {
		return contract.SessionView{}, errSessionBusy
	}
	defer s.observationGate.Unlock()
	return s.core.SessionView(ctx)
}

func (s *Service) EnableConnectionEvents(ctx context.Context) error {
	return s.core.EnableConnectionEvents(ctx)
}

func (s *Service) AcknowledgeConnection(ctx context.Context, host string, revision int64) error {
	return s.core.AcknowledgeConnection(ctx, host, revision)
}

func (s *Service) AcknowledgeCoreEvent(ctx context.Context, backend string, sequence int64) error {
	return s.core.AcknowledgeCoreEvent(ctx, backend, sequence)
}

func (s *Service) WaitConnection(ctx context.Context, after, afterSequence int64) (contract.ConnectionNotice, error) {
	if !s.watchGate.TryLock() {
		return contract.ConnectionNotice{}, errSessionBusy
	}
	defer s.watchGate.Unlock()
	return s.core.WaitConnection(ctx, after, afterSequence)
}

func (s *Service) BeginSessionCheck() (string, error) {
	s.ticketMu.Lock()
	defer s.ticketMu.Unlock()
	if s.ticket != uuid.Nil && time.Now().Before(s.ticketUntil) {
		return s.ticket.String(), nil
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return "", errSessionUnavailable
	}
	s.ticket, s.ticketUntil = id, time.Now().Add(4*time.Second)
	return id.String(), nil
}

func (s *Service) KeepAlive(ctx context.Context, token string) error {
	id, err := uuid.Parse(token)
	if err != nil {
		return errSessionUnavailable
	}
	s.ticketMu.Lock()
	deadline := s.ticketUntil
	valid := id == s.ticket && id != uuid.Nil && time.Now().Before(deadline)
	if valid {
		s.ticket = uuid.Nil
	}
	s.ticketMu.Unlock()
	if !valid {
		return errSessionUnavailable
	}
	if !s.sessionGate.TryLock() {
		return errSessionBusy
	}
	defer s.sessionGate.Unlock()
	return s.core.KeepAliveUntil(ctx, deadline)
}

func (s *Service) Snapshot(ctx context.Context) (contract.Snapshot, error) {
	return s.core.Snapshot(ctx)
}

func (s *Service) ListTests(ctx context.Context, params contract.ListTestsParams) (contract.TestList, error) {
	return s.core.ListTests(ctx, params)
}

func dispatch(result contract.CommandStatus, err error) (contract.CommandDispatch, error) {
	if errors.Is(err, errCommandNotSent) {
		return contract.CommandDispatch{Delivery: contract.CommandDispatchDeliveryNotSent}, nil
	}
	if err != nil {
		err = commandError(err)
		if errors.Is(err, ErrCommandConflict) {
			return contract.CommandDispatch{Delivery: contract.CommandDispatchDeliveryConflict}, nil
		}
		return contract.CommandDispatch{}, err
	}
	return contract.CommandDispatch{Delivery: contract.CommandDispatchDeliveryResponse, Command: &result}, nil
}

func commandError(err error) error {
	var failure *transport.RequestError
	if errors.As(err, &failure) && failure.Status == 409 && failure.Code == "command_conflict" && !failure.OutcomeUnknown {
		return errors.Join(ErrCommandConflict, err)
	}
	return err
}

func (s *Service) CreateTest(ctx context.Context, command CreateCommandInput) (contract.CommandDispatch, error) {
	return dispatch(s.core.CreateTestInput(ctx, command))
}

func (s *Service) Test(ctx context.Context, testID string) (contract.TestRecord, error) {
	return s.core.Test(ctx, testID)
}

func (s *Service) Readiness(ctx context.Context, testID string) (contract.Readiness, error) {
	return s.core.Readiness(ctx, testID)
}

func (s *Service) StartTest(ctx context.Context, testID string, command StartCommandInput) (contract.CommandDispatch, error) {
	return dispatch(s.core.StartTestInput(ctx, testID, command))
}

func (s *Service) StopTest(ctx context.Context, testID string, command StopCommandInput) (contract.CommandDispatch, error) {
	return dispatch(s.core.StopTestInput(ctx, testID, command))
}

func (s *Service) ConfirmAction(ctx context.Context, testID, actionID string, command ConfirmCommandInput) (contract.CommandDispatch, error) {
	return dispatch(s.core.ConfirmActionInput(ctx, testID, actionID, command))
}

func (s *Service) Measurements(ctx context.Context, testID string, after int64, limit int) (contract.MeasurementPage, error) {
	return s.core.Measurements(ctx, testID, after, limit)
}

func (s *Service) Audit(ctx context.Context, testID string, after int64, limit int) (contract.AuditPage, error) {
	return s.core.Audit(ctx, testID, after, limit)
}

func (s *Service) Command(ctx context.Context, commandID string) (contract.CommandStatus, error) {
	return s.core.Command(ctx, commandID)
}

// LookupCommandExact reads the saved attempt without retrying its action.
func (s *Service) LookupCommandExact(ctx context.Context, database, kind, testID, actionID, commandID, originalJSON string) (contract.ExactLookupResult, error) {
	if !s.lookupGate.TryLock() {
		return contract.ExactLookupResult{}, errSessionBusy
	}
	defer s.lookupGate.Unlock()
	return s.core.LookupCommandExact(ctx, database, kind, testID, actionID, commandID, originalJSON)
}

func init() {
	application.RegisterEvent[contract.Event]("core:event")
	application.RegisterEvent[contract.ConnectionState]("core:connection")
}
