// Package desktop defines the subject-specific boundary exposed by Wails.
package desktop

import (
	"context"

	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// Core is implemented by the authenticated native client, not by the frontend.
type Core interface {
	Handshake(context.Context) (contract.Handshake, error)
	ConnectionState(context.Context) (contract.ConnectionState, error)
	KeepAlive(context.Context) error
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
}

// Service forwards domain operations to the authenticated core client.
type Service struct {
	core Core
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

func (s *Service) KeepAlive(ctx context.Context) error {
	return s.core.KeepAlive(ctx)
}

func (s *Service) Snapshot(ctx context.Context) (contract.Snapshot, error) {
	return s.core.Snapshot(ctx)
}

func (s *Service) ListTests(ctx context.Context, params contract.ListTestsParams) (contract.TestList, error) {
	return s.core.ListTests(ctx, params)
}

func (s *Service) CreateTest(ctx context.Context, command CreateCommandInput) (contract.CommandStatus, error) {
	return s.core.CreateTestInput(ctx, command)
}

func (s *Service) Test(ctx context.Context, testID string) (contract.TestRecord, error) {
	return s.core.Test(ctx, testID)
}

func (s *Service) Readiness(ctx context.Context, testID string) (contract.Readiness, error) {
	return s.core.Readiness(ctx, testID)
}

func (s *Service) StartTest(ctx context.Context, testID string, command StartCommandInput) (contract.CommandStatus, error) {
	return s.core.StartTestInput(ctx, testID, command)
}

func (s *Service) StopTest(ctx context.Context, testID string, command StopCommandInput) (contract.CommandStatus, error) {
	return s.core.StopTestInput(ctx, testID, command)
}

func (s *Service) ConfirmAction(ctx context.Context, testID, actionID string, command ConfirmCommandInput) (contract.CommandStatus, error) {
	return s.core.ConfirmActionInput(ctx, testID, actionID, command)
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

func init() {
	application.RegisterEvent[contract.Event]("core:event")
	application.RegisterEvent[contract.ConnectionState]("core:connection")
}
