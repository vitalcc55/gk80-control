package desktop

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/vitalcc55/gk80-control/internal/contract"
)

// observationDelivery coalesces host observations for one Owner. It does not own
// process/session state. Readers and external sinks are called outside its locks.
type observationDelivery struct {
	ctx               context.Context
	status            func() contract.DesktopStatus
	connection        func() (contract.ConnectionState, bool)
	view              func(context.Context) (contract.SessionView, error)
	notify            func(contract.DesktopStatus)
	notifications     chan struct{}
	connectionChanges chan struct{}
	connectionMu      sync.Mutex
	connectionEmitter func(contract.ConnectionState)
	connectionActive  bool
	connectionPending *contract.ConnectionState
	connectionDirty   bool
	eventChanges      chan struct{}
	eventMu           sync.Mutex
	eventEmitter      func(contract.Event)
	eventPending      *contract.Event
	eventLatest       *contract.Event
	watchMu           sync.Mutex
	watchChanges      chan struct{}
	stopNotifications chan struct{}
	notifyStopOnce    sync.Once
}

func newObservationDelivery(ctx context.Context, status func() contract.DesktopStatus, connection func() (contract.ConnectionState, bool), view func(context.Context) (contract.SessionView, error), notify func(contract.DesktopStatus)) *observationDelivery {
	return &observationDelivery{ctx: ctx, status: status, connection: connection, view: view, notify: notify, notifications: make(chan struct{}, 1), connectionChanges: make(chan struct{}, 1), eventChanges: make(chan struct{}, 1), watchChanges: make(chan struct{}), stopNotifications: make(chan struct{})}
}
func (o *observationDelivery) start() {
	go o.notifyLoop()
	go o.connectionLoop()
	go o.eventLoop()
}

// Status delivery outlives context cancellation until Owner has observed workerDone.
// A blocked UI sink is deliberately not joined during shutdown.
func (o *observationDelivery) stopStatusNotifications() {
	o.notifyStopOnce.Do(func() { close(o.stopNotifications) })
}
func (o *observationDelivery) setCoreEventEmitter(emit func(contract.Event)) {
	o.eventMu.Lock()
	o.eventEmitter = emit
	o.eventMu.Unlock()
}

func (o *observationDelivery) observeCoreEvent(event contract.Event) {
	if event.Sequence <= 0 {
		return
	}
	o.eventMu.Lock()
	copy := event
	o.eventLatest = &copy
	o.eventMu.Unlock()
	select {
	case o.eventChanges <- struct{}{}:
	default:
	}
}

func (o *observationDelivery) eventLoop() {
	for {
		select {
		case <-o.ctx.Done():
			return
		case <-o.eventChanges:
			o.connectionMu.Lock()
			active := o.connectionActive
			o.connectionMu.Unlock()
			o.eventMu.Lock()
			if !active || o.eventEmitter == nil || o.eventLatest == nil || o.eventPending != nil {
				o.eventMu.Unlock()
				continue
			}
			copy := *o.eventLatest
			o.eventPending = &copy
			emit := o.eventEmitter
			o.eventMu.Unlock()
			emit(copy)
		}
	}
}

func (o *observationDelivery) acknowledgeCoreEvent(backend string, sequence int64) error {
	o.eventMu.Lock()
	if o.eventPending == nil || o.eventPending.BackendRunId.String() != backend || o.eventPending.Sequence != sequence {
		o.eventMu.Unlock()
		return errSessionUnavailable
	}
	o.eventPending = nil
	more := o.eventLatest != nil && o.eventLatest.Sequence > sequence
	o.eventMu.Unlock()
	if more {
		select {
		case o.eventChanges <- struct{}{}:
		default:
		}
	}
	return nil
}

func (o *observationDelivery) connectionNotice(view contract.SessionView, afterSequence int64) contract.ConnectionNotice {
	result := contract.ConnectionNotice{Connection: view.Connection}
	if view.Snapshot != nil {
		result.SnapshotBoundary = view.Snapshot.LastEventSequence
	}
	o.eventMu.Lock()
	defer o.eventMu.Unlock()
	if o.eventLatest != nil && o.eventLatest.Sequence > afterSequence {
		copy := *o.eventLatest
		result.Event = &copy
		result.Gap = copy.Sequence-afterSequence > 1
	}
	if result.SnapshotBoundary > afterSequence && (result.Event == nil || result.SnapshotBoundary > result.Event.Sequence) {
		result.Gap = true
	}
	return result
}

func (o *observationDelivery) setConnectionEmitter(emit func(contract.ConnectionState)) {
	o.connectionMu.Lock()
	o.connectionEmitter = emit
	o.connectionMu.Unlock()
}

func (o *observationDelivery) signalConnection() {
	o.watchMu.Lock()
	if o.watchChanges != nil {
		close(o.watchChanges)
	}
	o.watchChanges = make(chan struct{})
	o.watchMu.Unlock()
	select {
	case o.connectionChanges <- struct{}{}:
	default:
	}
}

func (o *observationDelivery) waitConnection(ctx context.Context, after, afterSequence int64) (contract.ConnectionNotice, error) {
	if after < 0 || afterSequence < 0 {
		return contract.ConnectionNotice{}, errSessionUnavailable
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return contract.ConnectionNotice{}, err
		}
		o.watchMu.Lock()
		changes := o.watchChanges
		o.watchMu.Unlock()
		view, err := o.view(waitCtx)
		if callerErr := ctx.Err(); callerErr != nil {
			return contract.ConnectionNotice{}, callerErr
		}
		if err != nil {
			return contract.ConnectionNotice{}, err
		}
		state := view.Connection
		if state.Revision > after {
			notice := o.connectionNotice(view, afterSequence)
			if err := ctx.Err(); err != nil {
				return contract.ConnectionNotice{}, err
			}
			return notice, nil
		}
		select {
		case <-changes:
		case <-waitCtx.Done():
			if err := ctx.Err(); err != nil {
				return contract.ConnectionNotice{}, err
			}
			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				view, err := o.view(ctx)
				if callerErr := ctx.Err(); callerErr != nil {
					return contract.ConnectionNotice{}, callerErr
				}
				if err != nil {
					return contract.ConnectionNotice{}, err
				}
				notice := o.connectionNotice(view, afterSequence)
				if err := ctx.Err(); err != nil {
					return contract.ConnectionNotice{}, err
				}
				return notice, nil
			}
			return contract.ConnectionNotice{}, waitCtx.Err()
		case <-o.ctx.Done():
			return contract.ConnectionNotice{}, errSessionUnavailable
		}
	}
}

func (o *observationDelivery) connectionLoop() {
	for {
		select {
		case <-o.ctx.Done():
			return
		case <-o.connectionChanges:
			state, available := o.connection()
			if !available {
				continue
			}
			o.connectionMu.Lock()
			if !o.connectionActive || o.connectionEmitter == nil {
				o.connectionMu.Unlock()
				continue
			}
			if o.connectionPending != nil {
				o.connectionDirty = true
				o.connectionMu.Unlock()
				continue
			}
			copy := state
			o.connectionPending = &copy
			emit := o.connectionEmitter
			o.connectionMu.Unlock()
			emit(state)
		}
	}
}

func (o *observationDelivery) enableConnectionEvents() error {
	o.connectionMu.Lock()
	o.connectionActive = true
	o.connectionMu.Unlock()
	o.signalConnection()
	select {
	case o.eventChanges <- struct{}{}:
	default:
	}
	return nil
}

func (o *observationDelivery) acknowledgeConnection(host string, revision int64) error {
	o.connectionMu.Lock()
	pending := o.connectionPending
	if pending == nil || pending.HostRunId.String() != host || pending.Revision != revision {
		o.connectionMu.Unlock()
		return errSessionUnavailable
	}
	o.connectionPending = nil
	dirty := o.connectionDirty
	o.connectionDirty = false
	o.connectionMu.Unlock()
	if dirty {
		o.signalConnection()
	}
	return nil
}

func (o *observationDelivery) notifyLoop() {
	for {
		select {
		case <-o.notifications:
			o.notify(o.status())
		case <-o.stopNotifications:
			return
		}
	}
}

func (o *observationDelivery) signalStatus() {
	select {
	case o.notifications <- struct{}{}:
	default:
	}
}
