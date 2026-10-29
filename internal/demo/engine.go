package demo

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/storage"
)

// Repository is the existing core-owned SQLite boundary used by the demo engine.
type Repository interface {
	Create(context.Context, contract.CreateTestCommand) (contract.CommandStatus, error)
	LookupCreate(context.Context, contract.CreateTestCommand) (contract.CommandStatus, error)
	CreateExact(context.Context, contract.CreateTestCommand, []byte) (contract.CommandStatus, error)
	LookupCreateExact(context.Context, contract.CreateTestCommand, []byte) (contract.CommandStatus, error)
	Revision(context.Context) (int64, error)
	Test(context.Context, uuid.UUID) (contract.TestRecord, error)
	Start(context.Context, uuid.UUID, contract.StartTestCommand, contract.PendingAction) (contract.CommandStatus, error)
	LookupStart(context.Context, uuid.UUID, contract.StartTestCommand) (contract.CommandStatus, error)
	RejectStartNotReady(context.Context, uuid.UUID, contract.StartTestCommand) (contract.CommandStatus, error)
	RejectStartUnavailable(context.Context, uuid.UUID, contract.StartTestCommand) (contract.CommandStatus, error)
	Confirm(context.Context, uuid.UUID, uuid.UUID, contract.ConfirmActionCommand) (contract.CommandStatus, error)
	LookupConfirm(context.Context, uuid.UUID, uuid.UUID, contract.ConfirmActionCommand) (contract.CommandStatus, error)
	RejectConfirmUnavailable(context.Context, uuid.UUID, uuid.UUID, contract.ConfirmActionCommand) (contract.CommandStatus, error)
	PendingAction(context.Context, uuid.UUID) (*contract.PendingAction, error)
	CompleteStart(context.Context, uuid.UUID, uuid.UUID) error
	RecordMeasurements(context.Context, uuid.UUID, []storage.TimedMeasurement) error
	Complete(context.Context, uuid.UUID, time.Duration) error
	FinishStop(context.Context, uuid.UUID, uuid.UUID, storage.StopDetails) (contract.CommandStatus, error)
	LookupStop(context.Context, uuid.UUID, uuid.UUID) (contract.CommandStatus, error)
}

// State is an internal observation, not yet the transport snapshot or event stream.
type State struct {
	BackendRunID  uuid.UUID
	Record        *contract.TestRecord
	PendingAction *contract.PendingAction
	Measurements  []contract.Measurement
	Stop          *contract.CommandStatus
	Revision      int64
}

type run struct {
	record         contract.TestRecord
	revision       int64
	pending        *contract.PendingAction
	latest         []contract.Measurement
	startID        uuid.UUID
	started        bool
	stopping       bool
	naturalDone    bool
	finishElapsed  time.Duration
	finalized      bool
	stopStatus     *contract.CommandStatus
	stopDone       chan struct{}
	stopDetails    storage.StopDetails
	writerDone     chan struct{}
	writerErr      error
	pendingSamples int
	ctx            context.Context
	cancel         context.CancelFunc
}

type pendingCreate struct {
	command  contract.CreateTestCommand
	identity []byte
}

type Engine struct {
	mu               sync.Mutex
	ordinary         chan struct{}
	store            Repository
	equipment        Equipment
	runID            uuid.UUID
	revision         int64
	unresolvedCreate *pendingCreate
	active           *run
	changed          chan struct{}
}

func NewEngine(ctx context.Context, store Repository, equipment Equipment, runID uuid.UUID) (*Engine, error) {
	revision, err := store.Revision(ctx)
	if err != nil {
		return nil, err
	}
	return &Engine{store: store, equipment: equipment, runID: runID, revision: revision, ordinary: make(chan struct{}, 1), changed: make(chan struct{}, 1)}, nil
}

func (e *Engine) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case e.ordinary <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrBusy
	}
}

var ErrBusy = errors.New("another command is being processed")

func (e *Engine) Changes() <-chan struct{} { return e.changed }
func (e *Engine) signal() {
	select {
	case e.changed <- struct{}{}:
	default:
	}
}

func (e *Engine) State() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	state := State{BackendRunID: e.runID, Revision: e.revision, Measurements: make([]contract.Measurement, 0)}
	if e.active == nil {
		return state
	}
	record := e.active.record
	if record.FinishedAt != nil {
		finished := *record.FinishedAt
		record.FinishedAt = &finished
	}
	state.Record = &record
	state.Revision = e.active.revision
	if e.active.pending != nil {
		action := *e.active.pending
		state.PendingAction = &action
	}
	if e.active.stopStatus != nil {
		status := copyCommand(*e.active.stopStatus)
		state.Stop = &status
	}
	for _, measurement := range e.active.latest {
		copy := measurement
		if copy.Value != nil {
			value := *copy.Value
			copy.Value = &value
		}
		if copy.MeasuredAt != nil {
			measured := *copy.MeasuredAt
			copy.MeasuredAt = &measured
		}
		state.Measurements = append(state.Measurements, copy)
	}
	return state
}

func copyCommand(status contract.CommandStatus) contract.CommandStatus {
	if status.TestId != nil {
		testID := *status.TestId
		status.TestId = &testID
	}
	if status.Reason != nil {
		reason := *status.Reason
		status.Reason = &reason
	}
	return status
}

func (e *Engine) Create(ctx context.Context, command contract.CreateTestCommand) (contract.CommandStatus, error) {
	return e.create(ctx, command, nil)
}

// CreateExact retains the identity of the validated JSON received by core.
func (e *Engine) CreateExact(ctx context.Context, command contract.CreateTestCommand, identity []byte) (contract.CommandStatus, error) {
	if len(identity) == 0 {
		return contract.CommandStatus{}, storage.ErrInvalidCommand
	}
	return e.create(ctx, command, append([]byte(nil), identity...))
}

func (e *Engine) create(ctx context.Context, command contract.CreateTestCommand, identity []byte) (contract.CommandStatus, error) {
	if err := e.acquire(ctx); err != nil {
		return contract.CommandStatus{}, err
	}
	defer func() { <-e.ordinary }()
	if err := ctx.Err(); err != nil {
		return contract.CommandStatus{}, err
	}
	if err := e.refreshCreated(ctx); err != nil {
		return contract.CommandStatus{}, err
	}
	var known contract.CommandStatus
	var lookupErr error
	if identity == nil {
		known, lookupErr = e.store.LookupCreate(ctx, command)
	} else {
		known, lookupErr = e.store.LookupCreateExact(ctx, command, identity)
	}
	if !errors.Is(lookupErr, storage.ErrCommandNotFound) {
		e.abortOnLookupError(ctx, lookupErr)
		return known, lookupErr
	}
	e.mu.Lock()
	uncertain := e.active != nil && (e.active.naturalDone || e.active.stopping) && !e.active.finalized
	e.mu.Unlock()
	if uncertain {
		return contract.CommandStatus{}, ErrBusy
	}
	commandCtx, cancelCommand := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelCommand()
	e.mu.Lock()
	pending := pendingCreate{command: command, identity: identity}
	e.unresolvedCreate = &pending
	e.mu.Unlock()
	var result contract.CommandStatus
	var err error
	if identity == nil {
		result, err = e.store.Create(commandCtx, command)
	} else {
		result, err = e.store.CreateExact(commandCtx, command, identity)
	}
	if err != nil || result.TestId == nil {
		if definiteDecision(err) {
			e.mu.Lock()
			e.unresolvedCreate = nil
			e.mu.Unlock()
		}
		e.abortOnStorageError(err)
		return result, err
	}
	readCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = e.refreshCreated(readCtx)
	if err != nil {
		return result, err
	}
	return result, nil
}

// Refresh resolves a committed create after an interrupted response without resending it.
func (e *Engine) Refresh(ctx context.Context) error {
	if err := e.acquire(ctx); err != nil {
		return err
	}
	defer func() { <-e.ordinary }()
	if err := e.refreshCreated(ctx); err != nil {
		return err
	}
	return e.refreshFinished(ctx)
}

func (e *Engine) refreshFinished(ctx context.Context) error {
	e.mu.Lock()
	current := e.active
	if current == nil || current.record.SaveStatus != "unknown" || (!current.naturalDone && !current.stopping) {
		e.mu.Unlock()
		return nil
	}
	if current.stopping {
		select {
		case <-current.stopDone:
		default:
			e.mu.Unlock()
			return nil
		}
	}
	testID, stopID, details, elapsed := current.record.TestId, current.record.StopCommandId, current.stopDetails, current.finishElapsed
	stopping := current.stopping
	e.mu.Unlock()
	if stopping {
		status, err := e.store.LookupStop(ctx, testID, stopID)
		if err != nil {
			return err
		}
		if status.Status == "unknown" {
			if _, err := e.store.FinishStop(ctx, testID, stopID, details); err != nil {
				return err
			}
		}
	}
	record, err := e.store.Test(ctx, testID)
	if err != nil {
		return err
	}
	if !stopping && record.Stage != "finished" {
		if err := e.store.Complete(ctx, testID, elapsed); err != nil {
			return err
		}
		record, err = e.store.Test(ctx, testID)
		if err != nil {
			return err
		}
	}
	if !stopping && record.Outcome != "completed" {
		return storage.ErrInvalidState
	}
	revision, err := e.store.Revision(ctx)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active == current {
		current.record = record
		current.finalized = true
		current.revision = revision
		e.revision = revision
		e.signal()
	}
	return nil
}

func (e *Engine) refreshCreated(ctx context.Context) error {
	e.mu.Lock()
	pending := e.unresolvedCreate
	e.mu.Unlock()
	if pending == nil {
		return nil
	}
	var status contract.CommandStatus
	var err error
	if pending.identity == nil {
		status, err = e.store.LookupCreate(ctx, pending.command)
	} else {
		status, err = e.store.LookupCreateExact(ctx, pending.command, pending.identity)
	}
	if errors.Is(err, storage.ErrCommandNotFound) || definiteDecision(err) {
		e.mu.Lock()
		e.unresolvedCreate = nil
		e.mu.Unlock()
		return nil
	}
	if err != nil {
		return err
	}
	if status.TestId == nil {
		return storage.ErrInvalidState
	}
	record, err := e.store.Test(ctx, *status.TestId)
	if err != nil {
		return err
	}
	revision, err := e.store.Revision(ctx)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.unresolvedCreate = nil
	if record.Stage == "preparing" && (e.active == nil || e.active.record.TestId != record.TestId) {
		runCtx, cancel := context.WithCancel(context.Background())
		e.active = &run{record: record, revision: revision, latest: make([]contract.Measurement, 0), ctx: runCtx, cancel: cancel}
		e.revision = e.active.revision
		e.signal()
	}
	return nil
}

// Readiness reports only checks the demo engine can actually establish.
func (e *Engine) Readiness(testID uuid.UUID) (contract.Readiness, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	current := e.active
	if current == nil || current.record.TestId != testID {
		return contract.Readiness{}, storage.ErrTestNotFound
	}
	coreReady := current.record.Stage == "preparing" && !current.stopping
	simulatorReady := e.equipment.Ready()
	return contract.Readiness{TestId: testID, Revision: current.revision, Ready: coreReady && simulatorReady, Checks: []contract.ReadinessCheck{
		{Code: "demo", Label: "Демонстрационный режим ядра", Ready: coreReady, Source: "core"},
		{Code: "simulator", Label: "Симулятор готов", Ready: simulatorReady, Source: "simulator"},
	}}, nil
}

func (e *Engine) Start(ctx context.Context, testID uuid.UUID, command contract.StartTestCommand) (contract.CommandStatus, error) {
	if err := e.acquire(ctx); err != nil {
		return contract.CommandStatus{}, err
	}
	defer func() { <-e.ordinary }()
	previous, lookupErr := e.store.LookupStart(ctx, testID, command)
	if !errors.Is(lookupErr, storage.ErrCommandNotFound) {
		e.abortOnLookupError(ctx, lookupErr)
		return previous, lookupErr
	}
	e.mu.Lock()
	current := e.active
	if current == nil || current.record.TestId != testID {
		e.mu.Unlock()
		result, err := e.store.RejectStartUnavailable(ctx, testID, command)
		e.abortOnRejectedError(ctx, err)
		return result, err
	}
	if current.stopping {
		done := current.stopDone
		e.mu.Unlock()
		if err := waitForStop(ctx, done); err != nil {
			return contract.CommandStatus{}, err
		}
		e.mu.Lock()
		saved := current.finalized
		e.mu.Unlock()
		if !saved {
			return contract.CommandStatus{}, storage.ErrStorageUnavailable
		}
		action := contract.PendingAction{ActionId: uuid.New(), TestId: testID, Instruction: "Подтвердите начало демонстрационного измерения"}
		return e.store.Start(ctx, testID, command, action)
	}
	notReady := current.record.Stage == "preparing" && !e.equipment.Ready()
	opCtx, cancel := context.WithTimeout(current.ctx, 2*time.Second)
	e.mu.Unlock()
	defer cancel()
	if notReady {
		result, err := e.store.RejectStartNotReady(opCtx, testID, command)
		e.abortOnStorageError(err)
		return result, err
	}
	action := contract.PendingAction{ActionId: uuid.New(), TestId: testID, Instruction: "Подтвердите начало демонстрационного измерения"}
	status, err := e.store.Start(opCtx, testID, command, action)
	if err != nil {
		if unexpectedStorageError(err) {
			e.requestStop(current, "storage_error")
		}
		return status, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active == current && !current.stopping {
		current.record.Stage = "running"
		current.revision = command.ExpectedRevision + 1
		e.revision = current.revision
		current.pending = &action
		current.startID = command.CommandId
		e.signal()
	}
	return status, nil
}

func (e *Engine) Confirm(ctx context.Context, testID, actionID uuid.UUID, command contract.ConfirmActionCommand) (contract.CommandStatus, error) {
	if err := e.acquire(ctx); err != nil {
		return contract.CommandStatus{}, err
	}
	defer func() { <-e.ordinary }()
	previous, lookupErr := e.store.LookupConfirm(ctx, testID, actionID, command)
	if !errors.Is(lookupErr, storage.ErrCommandNotFound) {
		e.abortOnLookupError(ctx, lookupErr)
		return previous, lookupErr
	}
	e.mu.Lock()
	current := e.active
	if current == nil || current.record.TestId != testID {
		e.mu.Unlock()
		result, err := e.store.RejectConfirmUnavailable(ctx, testID, actionID, command)
		e.abortOnRejectedError(ctx, err)
		return result, err
	}
	if current.stopping {
		done := current.stopDone
		e.mu.Unlock()
		if err := waitForStop(ctx, done); err != nil {
			return contract.CommandStatus{}, err
		}
		e.mu.Lock()
		saved := current.finalized
		e.mu.Unlock()
		if !saved {
			return contract.CommandStatus{}, storage.ErrStorageUnavailable
		}
		return e.store.Confirm(ctx, testID, actionID, command)
	}
	opCtx, cancel := context.WithTimeout(current.ctx, 2*time.Second)
	e.mu.Unlock()
	defer cancel()
	status, err := e.store.Confirm(opCtx, testID, actionID, command)
	if err != nil {
		if unexpectedStorageError(err) {
			e.requestStop(current, "storage_error")
		}
		return status, err
	}
	e.mu.Lock()
	if e.active != current || current.stopping || current.started {
		e.mu.Unlock()
		return status, nil
	}
	if current.pending == nil || current.pending.ActionId != actionID {
		e.mu.Unlock()
		return status, nil
	}
	if err := e.equipment.Start(current.record.Parameters.PmaxPa); err != nil {
		e.acceptStopLocked(current, "system", "simulator_error")
		e.mu.Unlock()
		go e.finishStop(current)
		return status, err
	}
	current.pending = nil
	current.started = true
	current.revision = command.ExpectedRevision + 1
	e.revision = current.revision
	current.record.SaveStatus = "pending"
	e.signal()
	e.mu.Unlock()
	completeCtx, completeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	err = e.store.CompleteStart(completeCtx, testID, current.startID)
	completeCancel()
	if err != nil {
		e.requestStop(current, "storage_error")
		return status, err
	}
	e.mu.Lock()
	if e.active != current || current.stopping {
		e.mu.Unlock()
		return status, nil
	}
	current.record.SaveStatus = "saved"
	current.revision++
	e.revision = current.revision
	e.signal()
	e.mu.Unlock()
	go e.measure(current)
	return status, nil
}

func unexpectedStorageError(err error) bool {
	return err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, storage.ErrInvalidCommand) && !errors.Is(err, storage.ErrRevision) && !errors.Is(err, storage.ErrActiveTest) && !errors.Is(err, storage.ErrInvalidState) && !errors.Is(err, storage.ErrActionMismatch) && !errors.Is(err, storage.ErrTestNotFound) && !errors.Is(err, storage.ErrCommandConflict) && !errors.Is(err, storage.ErrNotReady)
}

func definiteDecision(err error) bool {
	return errors.Is(err, storage.ErrInvalidCommand) || errors.Is(err, storage.ErrRevision) || errors.Is(err, storage.ErrActiveTest) || errors.Is(err, storage.ErrInvalidState) || errors.Is(err, storage.ErrActionMismatch) || errors.Is(err, storage.ErrTestNotFound) || errors.Is(err, storage.ErrCommandConflict) || errors.Is(err, storage.ErrNotReady)
}

func (e *Engine) abortOnStorageError(err error) {
	if !unexpectedStorageError(err) {
		return
	}
	e.mu.Lock()
	current := e.active
	e.mu.Unlock()
	if current != nil {
		e.requestStop(current, "storage_error")
	}
}

func (e *Engine) abortOnLookupError(ctx context.Context, err error) {
	if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return
	}
	e.abortOnStorageError(err)
}

func (e *Engine) abortOnRejectedError(ctx context.Context, err error) {
	if ctx.Err() != nil && errors.Is(err, storage.ErrWriteNotStarted) && errors.Is(err, ctx.Err()) {
		return
	}
	e.abortOnStorageError(err)
}

// Stop marks acceptance without waiting for SQLite or the ordinary command queue.
func (e *Engine) Stop(ctx context.Context, testID, commandID uuid.UUID) (contract.CommandStatus, error) {
	e.mu.Lock()
	current := e.active
	if current == nil || current.record.TestId != testID || current.record.StopCommandId != commandID {
		e.mu.Unlock()
		return e.store.LookupStop(ctx, testID, commandID)
	}
	if current.stopping {
		result := copyCommand(*current.stopStatus)
		e.mu.Unlock()
		return result, nil
	}
	if current.naturalDone || current.record.Stage == "finished" {
		e.mu.Unlock()
		return contract.CommandStatus{}, storage.ErrInvalidState
	}
	result := e.acceptStopLocked(current, "demo-operator", "operator")
	e.mu.Unlock()
	go e.finishStop(current)
	return copyCommand(result), nil
}

func (e *Engine) acceptStopLocked(current *run, actor, reason string) contract.CommandStatus {
	current.stopping = true
	current.stopDetails = storage.StopDetails{Actor: actor, Reason: reason, AcceptedAt: time.Now().UTC()}
	current.stopDone = make(chan struct{})
	current.cancel()
	current.record.Stage = "stopping"
	current.record.SaveStatus = "pending"
	current.revision++
	e.revision = current.revision
	current.pending = nil
	id := current.record.TestId
	result := contract.CommandStatus{CommandId: current.record.StopCommandId, TestId: &id, Status: "accepted"}
	current.stopStatus = &result
	e.signal()
	return result
}

func (e *Engine) requestStop(current *run, reason string) {
	e.mu.Lock()
	if e.active != current || current.stopping || current.naturalDone {
		e.mu.Unlock()
		return
	}
	e.acceptStopLocked(current, "system", reason)
	e.mu.Unlock()
	go e.finishStop(current)
}

func (e *Engine) finishStop(current *run) {
	defer close(current.stopDone)
	e.mu.Lock()
	if e.active != current {
		e.mu.Unlock()
		return
	}
	err := e.equipment.Stop()
	if err != nil {
		current.stopStatus.Status = "failed"
		current.record.Outcome = "unknown"
		current.stopDetails.Failed = true
		current.stopDetails.Reason += ";simulator_stop_failed"
	} else {
		current.stopStatus.Status = "completed"
		current.record.Stage = "finished"
		current.record.Outcome = "interrupted"
	}
	current.record.DataQuality = "incomplete"
	current.record.SaveStatus = "pending"
	current.revision++
	e.revision = current.revision
	testID, stopID := current.record.TestId, current.record.StopCommandId
	writerDone := current.writerDone
	e.signal()
	e.mu.Unlock()
	if writerDone != nil {
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-writerDone:
			timer.Stop()
		case <-timer.C:
			e.mu.Lock()
			current.record.SaveStatus = "unknown"
			e.signal()
			e.mu.Unlock()
			<-writerDone
		}
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = e.store.FinishStop(stopCtx, testID, stopID, current.stopDetails)
	committed := err == nil
	var persisted contract.TestRecord
	var revision int64
	if err == nil {
		persisted, err = e.store.Test(stopCtx, testID)
	}
	if err == nil {
		revision, err = e.store.Revision(stopCtx)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active != current {
		return
	}
	if err != nil {
		current.record.SaveStatus = saveFailure(err, committed)
	} else {
		current.record = persisted
		current.finalized = true
		current.revision = revision
		e.revision = revision
	}
	e.signal()
}

func waitForStop(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

const sampleQueueLimit = 32
const measurementWriteTimeout = 2 * time.Second

type samplePacket []storage.TimedMeasurement

func (e *Engine) measure(current *run) {
	e.mu.Lock()
	if e.active != current || current.stopping {
		e.mu.Unlock()
		return
	}
	testID := current.record.TestId
	duration := time.Duration(math.Ceil(current.record.Parameters.MeasurementSeconds * float64(time.Second)))
	e.mu.Unlock()
	queue := make(chan samplePacket, sampleQueueLimit)
	e.mu.Lock()
	if e.active != current || current.stopping {
		e.mu.Unlock()
		return
	}
	current.writerDone = make(chan struct{})
	e.mu.Unlock()
	go e.writeSamples(current, testID, queue)
	var started time.Time
	sequence := int64(0)
	emit := func() (time.Duration, bool) {
		e.mu.Lock()
		if e.active != current || current.stopping {
			e.mu.Unlock()
			return 0, false
		}
		sampledAt := time.Now()
		if started.IsZero() {
			started = sampledAt
		}
		elapsed := sampledAt.Sub(started)
		pressure, dd2, dd3, err := e.equipment.Observe(elapsed)
		if err != nil {
			e.acceptStopLocked(current, "system", "simulator_error")
			e.mu.Unlock()
			go e.finishStop(current)
			return 0, false
		}
		now := sampledAt.UTC()
		measured := now.Format(time.RFC3339Nano)
		values := []struct {
			channel  contract.MeasurementChannel
			quantity contract.MeasurementQuantity
			value    float64
		}{{"dd1", "pressure", pressure}, {"dd2", "differentialPressure", dd2}, {"dd3", "differentialPressure", dd3}}
		packet := make(samplePacket, 0, len(values))
		latest := make([]contract.Measurement, 0, len(values))
		for _, value := range values {
			sequence++
			v := value.value
			sample := contract.Measurement{Sequence: sequence, Channel: value.channel, Quantity: value.quantity, Value: &v, Unit: "Pa", ObservedAt: now, MeasuredAt: &measured, Quality: "valid", Source: "simulator"}
			latest = append(latest, sample)
			packet = append(packet, storage.TimedMeasurement{Measurement: sample, Elapsed: elapsed})
		}
		select {
		case queue <- packet:
			current.pendingSamples += len(packet)
			current.record.SaveStatus = "pending"
			current.latest = latest
			e.signal()
			e.mu.Unlock()
			return elapsed, true
		default:
			e.acceptStopLocked(current, "system", "measurement_queue_full:3")
			current.stopDetails.MeasurementsLost = true
			e.mu.Unlock()
			go e.finishStop(current)
			return 0, false
		}
	}
	if _, ok := emit(); !ok {
		close(queue)
		return
	}
	interval := min(time.Second, duration)
	timer := time.NewTimer(interval)
	defer timer.Stop()
	var elapsed time.Duration
	for {
		select {
		case <-current.ctx.Done():
			close(queue)
			return
		case <-timer.C:
			var ok bool
			elapsed, ok = emit()
			if !ok {
				close(queue)
				return
			}
			if elapsed >= duration {
				close(queue)
				goto flush
			}
			timer.Reset(min(time.Second, duration-elapsed))
		}
	}
flush:
	select {
	case <-current.ctx.Done():
		return
	case <-current.writerDone:
		e.mu.Lock()
		err := current.writerErr
		e.mu.Unlock()
		if err != nil {
			e.requestStop(current, "storage_error")
			return
		}
	}
	e.mu.Lock()
	if e.active != current || current.stopping {
		e.mu.Unlock()
		return
	}
	if err := e.equipment.Finish(); err != nil {
		e.acceptStopLocked(current, "system", "simulator_error")
		e.mu.Unlock()
		go e.finishStop(current)
		return
	}
	current.naturalDone = true
	current.finishElapsed = elapsed
	current.record.Stage = "finished"
	current.record.Outcome = "completed"
	current.record.DataQuality = "pending"
	current.record.SaveStatus = "pending"
	current.revision++
	e.revision = current.revision
	e.signal()
	e.mu.Unlock()
	completeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := e.store.Complete(completeCtx, testID, elapsed)
	committed := err == nil
	var persisted contract.TestRecord
	var revision int64
	if err == nil {
		persisted, err = e.store.Test(completeCtx, testID)
	}
	if err == nil {
		revision, err = e.store.Revision(completeCtx)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active != current {
		return
	}
	if err != nil {
		current.record.SaveStatus = saveFailure(err, committed)
		current.record.DataQuality = "unknown"
	} else {
		current.record = persisted
		current.finalized = true
		current.revision = revision
		e.revision = revision
	}
	e.signal()
}

func saveFailure(err error, committed bool) contract.TestRecordSaveStatus {
	if !committed && errors.Is(err, storage.ErrStorageUnavailable) {
		return "failed"
	}
	return "unknown"
}

func (e *Engine) writeSamples(current *run, testID uuid.UUID, queue <-chan samplePacket) {
	defer close(current.writerDone)
	batch := make([]storage.TimedMeasurement, 0, 96)
	for packet := range queue {
		batch = append(batch[:0], packet...)
		closed := false
		for len(batch) <= 125 {
			select {
			case more, ok := <-queue:
				if !ok {
					closed = true
					goto save
				}
				batch = append(batch, more...)
			default:
				goto save
			}
		}
	save:
		pending := true
		watchdog := time.AfterFunc(measurementWriteTimeout, func() {
			e.mu.Lock()
			stop := pending && e.active == current && !current.stopping && !current.naturalDone
			if stop {
				e.acceptStopLocked(current, "system", "measurement_write_timeout")
			}
			e.mu.Unlock()
			if stop {
				go e.finishStop(current)
			}
		})
		err := e.store.RecordMeasurements(context.Background(), testID, batch)
		e.mu.Lock()
		pending = false
		e.mu.Unlock()
		watchdog.Stop()
		if err != nil {
			e.mu.Lock()
			current.writerErr = err
			startStop := false
			if current.stopping {
				current.stopDetails.Reason += ";measurement_write_failed"
			} else if e.active == current && !current.naturalDone {
				e.acceptStopLocked(current, "system", "measurement_write_failed")
				startStop = true
			}
			current.stopDetails.MeasurementsLost = true
			e.mu.Unlock()
			if startStop {
				go e.finishStop(current)
			}
			return
		}
		e.mu.Lock()
		current.pendingSamples -= len(batch)
		if current.pendingSamples == 0 && !current.stopping && !current.naturalDone {
			current.record.SaveStatus = "saved"
		}
		e.signal()
		e.mu.Unlock()
		if closed {
			return
		}
	}
}
