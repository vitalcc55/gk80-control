package demo

import (
	"context"
	"errors"
	"fmt"
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
	finishDone     chan struct{}
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
	command    contract.CreateTestCommand
	identity   []byte
	stopReason string
}

type Engine struct {
	mu               sync.Mutex
	ordinary         chan struct{}
	store            Repository
	equipment        Equipment
	runID            uuid.UUID
	revision         int64
	unresolvedCreate *pendingCreate
	pendingCommand   uuid.UUID
	active           *run
	changed          chan struct{}
	controller       *Controller
	closed           bool
	resolving        bool
	lifeCtx          context.Context
	lifeCancel       context.CancelFunc
	commandEvents    chan CommandEvent
	commandDrops     chan struct{}
	droppedInfo      uint64
	droppedError     uint64
}

func NewEngine(ctx context.Context, store Repository, equipment Equipment, runID uuid.UUID) (*Engine, error) {
	revision, err := store.Revision(ctx)
	if err != nil {
		return nil, err
	}
	lifeCtx, lifeCancel := context.WithCancel(context.Background())
	return &Engine{store: store, equipment: equipment, runID: runID, revision: revision, ordinary: make(chan struct{}, 1), changed: make(chan struct{}, 1), lifeCtx: lifeCtx, lifeCancel: lifeCancel}, nil
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
var ErrControlRevoked = errors.New("control has been revoked")
var ErrShutdownUnconfirmed = errors.New("engine shutdown is unconfirmed")

// Controller ties mutating commands to one UI ownership period.
type Controller struct {
	engine    *Engine
	revoked   bool      // guarded by engine.mu
	expiresAt time.Time // guarded by engine.mu
	ctx       context.Context
	cancel    context.CancelFunc
	timer     *time.Timer // guarded by engine.mu
}

func (e *Engine) GrantControl() (*Controller, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.grantControlLocked()
}

func (e *Engine) grantControlLocked() (*Controller, error) {
	if e.closed {
		return nil, ErrControlRevoked
	}
	if e.controller != nil && !e.controller.revoked {
		return nil, ErrBusy
	}
	controlCtx, cancel := context.WithCancel(context.Background())
	control := &Controller{engine: e, ctx: controlCtx, cancel: cancel}
	e.controller = control
	return control, nil
}

// GrantTimedControl atomically admits a controller and its initial deadline.
func (e *Engine) GrantTimedControl(ctx context.Context, duration time.Duration) (*Controller, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return nil, time.Time{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, time.Time{}, err
	}
	control, err := e.grantControlLocked()
	if err != nil {
		return nil, time.Time{}, err
	}
	return control, control.renewLocked(duration), nil
}

func (e *Engine) allowedLocked(control *Controller) bool {
	return !e.closed && (control == nil || (e.controller == control && !control.revoked && (control.expiresAt.IsZero() || time.Now().Before(control.expiresAt))))
}

// Renew ties command admission to the lease deadline, independently of timer scheduling.
func (c *Controller) Renew(duration time.Duration) (time.Time, error) {
	return c.RenewContext(context.Background(), duration)
}

// RenewContext rejects cancellation before changing the controller deadline.
func (c *Controller) RenewContext(ctx context.Context, duration time.Duration) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	c.engine.mu.Lock()
	defer c.engine.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	if !c.engine.allowedLocked(c) {
		return time.Time{}, ErrControlRevoked
	}
	return c.renewLocked(duration), nil
}

func (c *Controller) renewLocked(duration time.Duration) time.Time {
	c.expiresAt = time.Now().Add(duration)
	if c.timer == nil {
		c.timer = time.AfterFunc(time.Until(c.expiresAt), c.expire)
	} else {
		c.timer.Reset(time.Until(c.expiresAt))
	}
	return c.expiresAt
}

func (c *Controller) expire() {
	e := c.engine
	e.mu.Lock()
	if c.revoked || e.closed || e.controller != c {
		e.mu.Unlock()
		return
	}
	if remaining := time.Until(c.expiresAt); remaining > 0 {
		c.timer.Reset(remaining)
		e.mu.Unlock()
		return
	}
	c.revoked = true
	c.cancel()
	current := e.stopForLossLocked("ui_lost")
	resolve := e.unresolvedCreate != nil
	e.mu.Unlock()
	if current != nil {
		go e.finishStop(current)
	}
	if resolve {
		go e.resolveRevokedCreate()
	}
}

func (c *Controller) Done() <-chan struct{}    { return c.ctx.Done() }
func (c *Controller) Context() context.Context { return c.ctx }

func (e *Engine) allowed(control *Controller) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.allowedLocked(control)
}

type controlOperationContext struct {
	context.Context
	control *Controller
	source  context.Context
	cancel  context.CancelCauseFunc
}

// Err also checks the current deadline when a waiting write is ready to begin.
func (ctx controlOperationContext) Err() error {
	if err := ctx.source.Err(); err != nil {
		ctx.cancel(err)
	}
	if err := ctx.Context.Err(); err != nil {
		if errors.Is(context.Cause(ctx.Context), context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		return err
	}
	e := ctx.control.engine
	e.mu.Lock()
	expired := !ctx.control.expiresAt.IsZero() && !time.Now().Before(ctx.control.expiresAt)
	e.mu.Unlock()
	if expired {
		ctx.control.expire()
	}
	if errors.Is(context.Cause(ctx.Context), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ctx.Context.Err()
}

// operationContextLocked cancels a not-yet-started write when its UI ownership ends.
func operationContextLocked(ctx context.Context, control *Controller) (context.Context, func()) {
	if control == nil {
		return ctx, func() {}
	}
	operation, cancel := context.WithCancelCause(control.ctx)
	stop := context.AfterFunc(ctx, func() { cancel(ctx.Err()) })
	if err := ctx.Err(); err != nil {
		cancel(err)
	}
	return controlOperationContext{Context: operation, control: control, source: ctx, cancel: cancel}, func() { stop(); cancel(context.Canceled) }
}

// A database transaction may return Canceled even when its parent timed out.
func operationError(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errors.Join(err, context.DeadlineExceeded)
	}
	return err
}

func (c *Controller) Create(ctx context.Context, cmd contract.CreateTestCommand) (contract.CommandStatus, error) {
	return c.engine.create(ctx, cmd, nil, c, nil)
}

func (c *Controller) CreateExact(ctx context.Context, cmd contract.CreateTestCommand, identity []byte) (contract.CommandStatus, error) {
	if len(identity) == 0 {
		return contract.CommandStatus{}, storage.ErrInvalidCommand
	}
	return c.engine.create(ctx, cmd, append([]byte(nil), identity...), c, nil)
}

func (c *Controller) CreateExactWithDisposition(ctx context.Context, cmd contract.CreateTestCommand, identity []byte) (contract.CommandStatus, bool, error) {
	if len(identity) == 0 {
		return contract.CommandStatus{}, false, storage.ErrInvalidCommand
	}
	replayed := false
	result, err := c.engine.create(ctx, cmd, append([]byte(nil), identity...), c, &replayed)
	return result, replayed, err
}

// LookupCreateExact resolves replays without executing a new command.
func (c *Controller) LookupCreateExact(ctx context.Context, cmd contract.CreateTestCommand, identity []byte) (contract.CommandStatus, error) {
	return c.lookup(ctx, func() (contract.CommandStatus, error) {
		return c.engine.store.LookupCreateExact(ctx, cmd, identity)
	})
}

func (c *Controller) LookupStart(ctx context.Context, id uuid.UUID, cmd contract.StartTestCommand) (contract.CommandStatus, error) {
	return c.lookup(ctx, func() (contract.CommandStatus, error) {
		return c.engine.store.LookupStart(ctx, id, cmd)
	})
}

func (c *Controller) LookupConfirm(ctx context.Context, id, action uuid.UUID, cmd contract.ConfirmActionCommand) (contract.CommandStatus, error) {
	return c.lookup(ctx, func() (contract.CommandStatus, error) {
		return c.engine.store.LookupConfirm(ctx, id, action, cmd)
	})
}

func (c *Controller) lookup(ctx context.Context, read func() (contract.CommandStatus, error)) (contract.CommandStatus, error) {
	e := c.engine
	e.mu.Lock()
	if !e.allowedLocked(c) {
		e.mu.Unlock()
		return contract.CommandStatus{}, ErrControlRevoked
	}
	current := e.active
	e.mu.Unlock()
	result, err := read()
	e.mu.Lock()
	if !e.allowedLocked(c) {
		e.mu.Unlock()
		return contract.CommandStatus{}, ErrControlRevoked
	}
	stop := !errors.Is(err, storage.ErrCommandNotFound) && unexpectedStorageError(err) &&
		(ctx.Err() == nil || !errors.Is(err, ctx.Err())) &&
		current != nil && e.active == current && !current.stopping && !current.naturalDone
	if stop {
		e.acceptStopLocked(current, "system", "storage_error")
	}
	e.mu.Unlock()
	if stop {
		go e.finishStop(current)
	}
	return result, err
}

func (c *Controller) Start(ctx context.Context, id uuid.UUID, cmd contract.StartTestCommand) (contract.CommandStatus, error) {
	return c.engine.start(ctx, id, cmd, c, nil)
}

func (c *Controller) Confirm(ctx context.Context, id, action uuid.UUID, cmd contract.ConfirmActionCommand) (contract.CommandStatus, error) {
	return c.engine.confirm(ctx, id, action, cmd, c, nil)
}

func (c *Controller) StartWithDisposition(ctx context.Context, id uuid.UUID, cmd contract.StartTestCommand) (contract.CommandStatus, bool, error) {
	replayed := false
	result, err := c.engine.start(ctx, id, cmd, c, &replayed)
	return result, replayed, err
}

func (c *Controller) ConfirmWithDisposition(ctx context.Context, id, action uuid.UUID, cmd contract.ConfirmActionCommand) (contract.CommandStatus, bool, error) {
	replayed := false
	result, err := c.engine.confirm(ctx, id, action, cmd, c, &replayed)
	return result, replayed, err
}

func (c *Controller) Stop(ctx context.Context, id, commandID uuid.UUID) (contract.CommandStatus, error) {
	return c.engine.stop(ctx, id, commandID, c)
}

// TryStop handles the active reserved stop without accessing storage.
func (c *Controller) TryStop(id, commandID uuid.UUID) (contract.CommandStatus, bool, bool, error) {
	return c.engine.tryStop(id, commandID, c)
}

func (c *Controller) LookupStop(ctx context.Context, id, commandID uuid.UUID) (contract.CommandStatus, error) {
	return c.lookup(ctx, func() (contract.CommandStatus, error) {
		return c.engine.store.LookupStop(ctx, id, commandID)
	})
}

// Revoke fences late commands before requesting the system stop.
func (c *Controller) Revoke(reason string) {
	e := c.engine
	e.mu.Lock()
	if e.controller != c || c.revoked {
		e.mu.Unlock()
		return
	}
	c.revoked = true
	c.cancel()
	if c.timer != nil {
		c.timer.Stop()
	}
	current := e.stopForLossLocked(reason)
	resolve := e.unresolvedCreate != nil
	e.mu.Unlock()
	if current != nil {
		go e.finishStop(current)
	}
	if resolve {
		go e.resolveRevokedCreate()
	}
}

// stopForLossLocked records the stop intent even while creation is unresolved.
func (e *Engine) stopForLossLocked(reason string) *run {
	if e.unresolvedCreate != nil && e.unresolvedCreate.stopReason == "" {
		e.unresolvedCreate.stopReason = reason
	}
	current := e.active
	if current == nil || current.stopping || current.naturalDone || current.finalized {
		return nil
	}
	e.acceptStopLocked(current, "system", reason)
	return current
}

// Shutdown fences new commands and waits only until ctx for confirmed persistence.
func (e *Engine) Shutdown(ctx context.Context, reason string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	e.mu.Lock()
	e.closed = true
	e.lifeCancel()
	if e.controller != nil {
		e.controller.revoked = true
		e.controller.cancel()
		if e.controller.timer != nil {
			e.controller.timer.Stop()
		}
	}
	current := e.stopForLossLocked(reason)
	e.mu.Unlock()
	if current != nil {
		go e.finishStop(current)
	}
	resolved := make(chan error, 1)
	go func() {
		for {
			if err := ctx.Err(); err != nil {
				resolved <- err
				return
			}
			select {
			case e.ordinary <- struct{}{}:
				if err := ctx.Err(); err != nil {
					<-e.ordinary
					resolved <- err
					return
				}
				err := e.refreshCreated(ctx)
				<-e.ordinary
				if err == nil {
					resolved <- nil
					return
				}
			case <-ctx.Done():
				resolved <- ctx.Err()
				return
			}
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				resolved <- ctx.Err()
				return
			}
		}
	}()
	select {
	case err := <-resolved:
		if err != nil {
			return fmt.Errorf("%w: %w", ErrShutdownUnconfirmed, err)
		}
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", ErrShutdownUnconfirmed, ctx.Err())
	}
	e.mu.Lock()
	current = e.active
	var done <-chan struct{}
	if current != nil && current.stopping {
		done = current.stopDone
	} else if current != nil && current.naturalDone {
		done = current.finishDone
	}
	e.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", ErrShutdownUnconfirmed, ctx.Err())
		}
	}
	e.mu.Lock()
	confirmed := e.unresolvedCreate == nil && (e.active == nil || (e.active.finalized && (e.active.stopStatus == nil || e.active.stopStatus.Status == "completed")))
	e.mu.Unlock()
	if !confirmed {
		return ErrShutdownUnconfirmed
	}
	return nil
}

func (e *Engine) Changes() <-chan struct{} { return e.changed }

type CommandEvent struct {
	Status   contract.CommandStatus
	ActionID *uuid.UUID
}

// CommandEvents returns a bounded, nonblocking diagnostic stream for one consumer and a loss signal.
func (e *Engine) CommandEvents() (<-chan CommandEvent, <-chan struct{}) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.commandEvents == nil {
		e.commandEvents = make(chan CommandEvent, 64)
		e.commandDrops = make(chan struct{}, 1)
	}
	return e.commandEvents, e.commandDrops
}

func (e *Engine) emitCommand(status contract.CommandStatus, action *uuid.UUID) {
	e.mu.Lock()
	events, drops := e.commandEvents, e.commandDrops
	e.mu.Unlock()
	if events == nil {
		return
	}
	event := CommandEvent{Status: copyCommand(status)}
	if action != nil {
		copy := *action
		event.ActionID = &copy
	}
	select {
	case events <- event:
	default:
		e.mu.Lock()
		if status.Status == "failed" {
			e.droppedError++
		} else {
			e.droppedInfo++
		}
		e.mu.Unlock()
		select {
		case drops <- struct{}{}:
		default:
		}
	}
}

func (e *Engine) TakeCommandDrops() (uint64, uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	info, failed := e.droppedInfo, e.droppedError
	e.droppedInfo, e.droppedError = 0, 0
	return info, failed
}

// NeedsRefresh reports recoverable in-memory uncertainty without performing I/O.
func (e *Engine) NeedsRefresh() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.unresolvedCreate != nil || (e.active != nil && e.active.record.SaveStatus == "unknown" && (e.active.naturalDone || e.active.stopping))
}

func (e *Engine) HasUnresolvedCreate() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.unresolvedCreate != nil
}

// CommandPending identifies an admitted command whose persisted outcome is unresolved.
func (e *Engine) CommandPending(id uuid.UUID) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.pendingCommand == id || (e.unresolvedCreate != nil && e.unresolvedCreate.command.CommandId == id)
}

func (e *Engine) clearPendingCommand(id uuid.UUID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pendingCommand == id {
		e.pendingCommand = uuid.Nil
	}
}
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
	return e.create(ctx, command, nil, nil, nil)
}

// CreateExact retains the identity of the validated JSON received by core.
func (e *Engine) CreateExact(ctx context.Context, command contract.CreateTestCommand, identity []byte) (contract.CommandStatus, error) {
	if len(identity) == 0 {
		return contract.CommandStatus{}, storage.ErrInvalidCommand
	}
	return e.create(ctx, command, append([]byte(nil), identity...), nil, nil)
}

func (e *Engine) create(ctx context.Context, command contract.CreateTestCommand, identity []byte, control *Controller, replayed *bool) (contract.CommandStatus, error) {
	if err := e.acquire(ctx); err != nil {
		return contract.CommandStatus{}, err
	}
	defer func() {
		<-e.ordinary
		e.mu.Lock()
		resolve := e.unresolvedCreate != nil && e.unresolvedCreate.stopReason != ""
		e.mu.Unlock()
		if resolve {
			go e.resolveRevokedCreate()
		}
	}()
	e.mu.Lock()
	allowed := e.allowedLocked(control)
	e.mu.Unlock()
	if !allowed {
		return contract.CommandStatus{}, ErrControlRevoked
	}
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
		if replayed != nil && known.CommandId != uuid.Nil {
			*replayed = true
		}
		e.abortOnLookupError(ctx, lookupErr)
		if !e.allowed(control) {
			return contract.CommandStatus{}, ErrControlRevoked
		}
		return known, lookupErr
	}
	e.mu.Lock()
	uncertain := e.active != nil && (e.active.naturalDone || e.active.stopping) && !e.active.finalized
	allowed = e.allowedLocked(control)
	e.mu.Unlock()
	if !allowed {
		return contract.CommandStatus{}, ErrControlRevoked
	}
	if uncertain {
		return contract.CommandStatus{}, ErrBusy
	}
	commandCtx, cancelCommand := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelCommand()
	e.mu.Lock()
	if !e.allowedLocked(control) {
		e.mu.Unlock()
		return contract.CommandStatus{}, ErrControlRevoked
	}
	if err := ctx.Err(); err != nil {
		e.mu.Unlock()
		return contract.CommandStatus{}, err
	}
	pending := pendingCreate{command: command, identity: identity}
	e.unresolvedCreate = &pending
	opCtx, cancelOperation := operationContextLocked(commandCtx, control)
	e.mu.Unlock()
	defer cancelOperation()
	var result contract.CommandStatus
	var err error
	if identity == nil {
		result, err = e.store.Create(opCtx, command)
	} else {
		result, err = e.store.CreateExact(opCtx, command, identity)
	}
	err = operationError(opCtx, err)
	if err != nil || result.TestId == nil {
		if definiteDecision(err) {
			e.mu.Lock()
			e.unresolvedCreate = nil
			e.mu.Unlock()
		}
		e.abortOnStorageError(err)
		return result, err
	}
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed {
		return result, ErrShutdownUnconfirmed
	}
	readCtx, cancel := context.WithTimeout(e.lifeCtx, 2*time.Second)
	defer cancel()
	err = e.refreshCreated(readCtx)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (e *Engine) resolveRevokedCreate() {
	e.mu.Lock()
	if e.resolving || e.closed {
		e.mu.Unlock()
		return
	}
	e.resolving = true
	e.mu.Unlock()
	for {
		ctx, cancel := context.WithTimeout(e.lifeCtx, 2*time.Second)
		select {
		case e.ordinary <- struct{}{}:
			e.mu.Lock()
			closed := e.closed
			e.mu.Unlock()
			if !closed {
				_ = e.refreshCreated(ctx)
			}
			<-e.ordinary
		case <-ctx.Done():
		}
		cancel()
		e.mu.Lock()
		pending := !e.closed && e.unresolvedCreate != nil && e.unresolvedCreate.stopReason != ""
		if !pending {
			e.resolving = false
		}
		e.mu.Unlock()
		if !pending {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
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
	if err := ctx.Err(); err != nil {
		return err
	}
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
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, storage.ErrCommandNotFound) || definiteDecision(err) {
		e.mu.Lock()
		if ctx.Err() != nil {
			e.mu.Unlock()
			return ctx.Err()
		}
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
	if err := ctx.Err(); err != nil {
		return err
	}
	revision, err := e.store.Revision(ctx)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	e.unresolvedCreate = nil
	if record.Stage == "preparing" && (e.active == nil || e.active.record.TestId != record.TestId) {
		runCtx, cancel := context.WithCancel(context.Background())
		e.active = &run{record: record, revision: revision, latest: make([]contract.Measurement, 0), ctx: runCtx, cancel: cancel}
		e.revision = e.active.revision
		e.signal()
		if pending.stopReason != "" {
			current := e.stopForLossLocked(pending.stopReason)
			if current != nil {
				go e.finishStop(current)
			}
		}
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
	return e.start(ctx, testID, command, nil, nil)
}

func (e *Engine) start(ctx context.Context, testID uuid.UUID, command contract.StartTestCommand, control *Controller, replayed *bool) (contract.CommandStatus, error) {
	if err := e.acquire(ctx); err != nil {
		return contract.CommandStatus{}, err
	}
	defer func() { <-e.ordinary }()
	e.mu.Lock()
	allowed := e.allowedLocked(control)
	e.mu.Unlock()
	if !allowed {
		return contract.CommandStatus{}, ErrControlRevoked
	}
	previous, lookupErr := e.store.LookupStart(ctx, testID, command)
	if !errors.Is(lookupErr, storage.ErrCommandNotFound) {
		if replayed != nil && previous.CommandId != uuid.Nil {
			*replayed = true
		}
		e.abortOnLookupError(ctx, lookupErr)
		if !e.allowed(control) {
			return contract.CommandStatus{}, ErrControlRevoked
		}
		return previous, lookupErr
	}
	e.mu.Lock()
	if !e.allowedLocked(control) {
		e.mu.Unlock()
		return contract.CommandStatus{}, ErrControlRevoked
	}
	if err := ctx.Err(); err != nil {
		e.mu.Unlock()
		return contract.CommandStatus{}, err
	}
	e.pendingCommand = command.CommandId
	defer e.clearPendingCommand(command.CommandId)
	current := e.active
	if control != nil && current != nil && current.stopping && !current.finalized {
		e.mu.Unlock()
		return contract.CommandStatus{}, ErrBusy
	}
	if current == nil || current.record.TestId != testID {
		opCtx, cancel := operationContextLocked(ctx, control)
		e.mu.Unlock()
		defer cancel()
		result, err := e.store.RejectStartUnavailable(opCtx, testID, command)
		err = operationError(opCtx, err)
		e.abortOnRejectedError(opCtx, err)
		if !e.allowed(control) {
			return contract.CommandStatus{}, ErrControlRevoked
		}
		return result, err
	}
	if current.stopping {
		done := current.stopDone
		if control != nil {
			opCtx, cancel := operationContextLocked(ctx, control)
			e.mu.Unlock()
			defer cancel()
			action := contract.PendingAction{ActionId: uuid.New(), TestId: testID, Instruction: "Подтвердите начало демонстрационного измерения"}
			result, err := e.store.Start(opCtx, testID, command, action)
			err = operationError(opCtx, err)
			if !e.allowed(control) {
				return contract.CommandStatus{}, ErrControlRevoked
			}
			return result, err
		}
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
	baseCtx, cancelBase := context.WithTimeout(current.ctx, 2*time.Second)
	opCtx, cancel := operationContextLocked(baseCtx, control)
	e.mu.Unlock()
	defer cancel()
	defer cancelBase()
	if notReady {
		result, err := e.store.RejectStartNotReady(opCtx, testID, command)
		err = operationError(opCtx, err)
		e.abortOnStorageError(err)
		return result, err
	}
	action := contract.PendingAction{ActionId: uuid.New(), TestId: testID, Instruction: "Подтвердите начало демонстрационного измерения"}
	status, err := e.store.Start(opCtx, testID, command, action)
	err = operationError(opCtx, err)
	if err != nil {
		if unexpectedStorageError(err) {
			e.requestStop(current, "storage_error")
		}
		return status, err
	}
	e.emitCommand(status, nil)
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
	return e.confirm(ctx, testID, actionID, command, nil, nil)
}

func (e *Engine) confirm(ctx context.Context, testID, actionID uuid.UUID, command contract.ConfirmActionCommand, control *Controller, replayed *bool) (contract.CommandStatus, error) {
	if err := e.acquire(ctx); err != nil {
		return contract.CommandStatus{}, err
	}
	defer func() { <-e.ordinary }()
	e.mu.Lock()
	allowed := e.allowedLocked(control)
	e.mu.Unlock()
	if !allowed {
		return contract.CommandStatus{}, ErrControlRevoked
	}
	previous, lookupErr := e.store.LookupConfirm(ctx, testID, actionID, command)
	if !errors.Is(lookupErr, storage.ErrCommandNotFound) {
		if replayed != nil && previous.CommandId != uuid.Nil {
			*replayed = true
		}
		e.abortOnLookupError(ctx, lookupErr)
		if !e.allowed(control) {
			return contract.CommandStatus{}, ErrControlRevoked
		}
		return previous, lookupErr
	}
	e.mu.Lock()
	if !e.allowedLocked(control) {
		e.mu.Unlock()
		return contract.CommandStatus{}, ErrControlRevoked
	}
	if err := ctx.Err(); err != nil {
		e.mu.Unlock()
		return contract.CommandStatus{}, err
	}
	e.pendingCommand = command.CommandId
	defer e.clearPendingCommand(command.CommandId)
	current := e.active
	if control != nil && current != nil && current.stopping && !current.finalized {
		e.mu.Unlock()
		return contract.CommandStatus{}, ErrBusy
	}
	if current == nil || current.record.TestId != testID {
		opCtx, cancel := operationContextLocked(ctx, control)
		e.mu.Unlock()
		defer cancel()
		result, err := e.store.RejectConfirmUnavailable(opCtx, testID, actionID, command)
		err = operationError(opCtx, err)
		e.abortOnRejectedError(opCtx, err)
		if !e.allowed(control) {
			return contract.CommandStatus{}, ErrControlRevoked
		}
		return result, err
	}
	if current.stopping {
		done := current.stopDone
		if control != nil {
			opCtx, cancel := operationContextLocked(ctx, control)
			e.mu.Unlock()
			defer cancel()
			result, err := e.store.Confirm(opCtx, testID, actionID, command)
			err = operationError(opCtx, err)
			if !e.allowed(control) {
				return contract.CommandStatus{}, ErrControlRevoked
			}
			return result, err
		}
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
	baseCtx, cancelBase := context.WithTimeout(current.ctx, 2*time.Second)
	opCtx, cancel := operationContextLocked(baseCtx, control)
	e.mu.Unlock()
	defer cancel()
	defer cancelBase()
	status, err := e.store.Confirm(opCtx, testID, actionID, command)
	err = operationError(opCtx, err)
	if err != nil {
		if unexpectedStorageError(err) {
			e.requestStop(current, "storage_error")
		}
		return status, err
	}
	e.emitCommand(status, &actionID)
	e.mu.Lock()
	if e.active != current || current.stopping || current.started || !e.allowedLocked(control) {
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
	completeDone := make(chan startCompletion, 1)
	deadline, _ := completeCtx.Deadline()
	go func() {
		err := e.store.CompleteStart(completeCtx, testID, current.startID)
		completeDone <- startCompletion{err: err, at: time.Now()}
	}()
	completed, timedOut := waitStartCompletion(completeCtx, deadline, completeDone, func() { e.requestStop(current, "storage_error") })
	if timedOut {
		if completed.err == nil {
			e.emitCommand(contract.CommandStatus{CommandId: current.startID, TestId: &testID, Status: "completed"}, &actionID)
		}
		completeCancel()
		return status, context.DeadlineExceeded
	}
	err = completed.err
	completeCancel()
	if err != nil {
		e.requestStop(current, "storage_error")
		return status, err
	}
	e.emitCommand(contract.CommandStatus{CommandId: current.startID, TestId: &testID, Status: "completed"}, &actionID)
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
	return err != nil && (!errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) && !errors.Is(err, storage.ErrInvalidCommand) && !errors.Is(err, storage.ErrRevision) && !errors.Is(err, storage.ErrActiveTest) && !errors.Is(err, storage.ErrInvalidState) && !errors.Is(err, storage.ErrActionMismatch) && !errors.Is(err, storage.ErrTestNotFound) && !errors.Is(err, storage.ErrCommandConflict) && !errors.Is(err, storage.ErrNotReady)
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
	return e.stop(ctx, testID, commandID, nil)
}

func (e *Engine) stop(ctx context.Context, testID, commandID uuid.UUID, control *Controller) (contract.CommandStatus, error) {
	result, matched, _, err := e.tryStop(testID, commandID, control)
	if matched {
		return result, err
	}
	result, err = e.store.LookupStop(ctx, testID, commandID)
	if !e.allowed(control) {
		return contract.CommandStatus{}, ErrControlRevoked
	}
	return result, err
}

func (e *Engine) tryStop(testID, commandID uuid.UUID, control *Controller) (contract.CommandStatus, bool, bool, error) {
	e.mu.Lock()
	if !e.allowedLocked(control) {
		e.mu.Unlock()
		return contract.CommandStatus{}, true, false, ErrControlRevoked
	}
	current := e.active
	if current == nil || current.record.TestId != testID || current.record.StopCommandId != commandID {
		e.mu.Unlock()
		return contract.CommandStatus{}, false, false, nil
	}
	if current.stopping {
		result := copyCommand(*current.stopStatus)
		e.mu.Unlock()
		return result, true, true, nil
	}
	if current.naturalDone || current.record.Stage == "finished" {
		e.mu.Unlock()
		return contract.CommandStatus{}, true, false, storage.ErrInvalidState
	}
	result := e.acceptStopLocked(current, "demo-operator", "operator")
	e.mu.Unlock()
	go e.finishStop(current)
	return copyCommand(result), true, false, nil
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
	accepted := copyCommand(*current.stopStatus)
	e.mu.Unlock()
	e.emitCommand(accepted, nil)
	e.mu.Lock()
	if e.active != current {
		e.mu.Unlock()
		return
	}
	err := e.equipment.Stop()
	if err != nil {
		current.stopStatus.Status = "failed"
		reason := "simulator_stop_failed"
		current.stopStatus.Reason = &reason
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
	stopStatus := copyCommand(*current.stopStatus)
	e.signal()
	e.mu.Unlock()
	e.emitCommand(stopStatus, nil)
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

type startCompletion struct {
	err error
	at  time.Time
}

func waitStartCompletion(ctx context.Context, deadline time.Time, done <-chan startCompletion, onTimeout func()) (startCompletion, bool) {
	var completed startCompletion
	select {
	case completed = <-done:
	case <-ctx.Done():
		select {
		case completed = <-done:
		default:
			onTimeout()
			return <-done, true
		}
	}
	if !completed.at.Before(deadline) {
		onTimeout()
		return completed, true
	}
	return completed, false
}

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
	current.finishDone = make(chan struct{})
	defer close(current.finishDone)
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
