package core

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/demo"
)

var errLeaseBusy = errors.New("another UI client owns the lease")
var errLeaseInvalid = errors.New("UI lease is invalid or expired")

type uiLease struct {
	id         uuid.UUID
	clientID   uuid.UUID
	expiresAt  time.Time // guarded by uiLeases.mu
	controller *demo.Controller
	done       <-chan struct{}
}

type uiLeases struct {
	mu       sync.Mutex
	engine   *demo.Engine
	duration time.Duration
	current  *uiLease
	closed   bool
}

func newUILeases(engine *demo.Engine, duration time.Duration) *uiLeases {
	return &uiLeases{engine: engine, duration: duration}
}

func (l *uiLeases) acquire(ctx context.Context, clientID, proof uuid.UUID) (*uiLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if clientID == uuid.Nil {
		return nil, errLeaseInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.expireLocked()
	if l.closed {
		return nil, errLeaseInvalid
	}
	if l.current != nil {
		if proof == l.current.id && clientID == l.current.clientID {
			return l.current, nil
		}
		return nil, errLeaseBusy
	}
	if proof != uuid.Nil {
		return nil, errLeaseInvalid
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return nil, err
	}
	controller, deadline, err := l.engine.GrantTimedControl(ctx, l.duration)
	if err != nil {
		return nil, err
	}
	lease := &uiLease{id: id, clientID: clientID, controller: controller, done: controller.Done(), expiresAt: deadline}
	l.current = lease
	if !time.Now().Before(deadline) {
		l.revokeLocked("ui_lost")
		return nil, errLeaseInvalid
	}
	return lease, nil
}

func (l *uiLeases) renew(ctx context.Context, id uuid.UUID) (*uiLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.expireLocked()
	if l.closed || l.current == nil || id != l.current.id {
		return nil, errLeaseInvalid
	}
	deadline, err := l.current.controller.RenewContext(ctx, l.duration)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		l.revokeLocked("ui_lost")
		return nil, errLeaseInvalid
	}
	l.current.expiresAt = deadline
	if !time.Now().Before(deadline) {
		l.revokeLocked("ui_lost")
		return nil, errLeaseInvalid
	}
	return l.current, nil
}

func (l *uiLeases) release(ctx context.Context, id uuid.UUID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	l.expireLocked()
	if l.closed || l.current == nil || id != l.current.id {
		return errLeaseInvalid
	}
	l.revokeLocked("ui_lost")
	return nil
}

func (l *uiLeases) view(lease *uiLease) (contract.UiLease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expireLocked()
	if l.closed || l.current != lease {
		return contract.UiLease{}, errLeaseInvalid
	}
	return contract.UiLease{LeaseId: lease.id, ClientId: lease.clientID, ExpiresAt: lease.expiresAt}, nil
}

func (l *uiLeases) owner(id uuid.UUID) (*uiLease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expireLocked()
	if l.closed || l.current == nil || l.current.id != id {
		return nil, errLeaseInvalid
	}
	select {
	case <-l.current.done:
		return nil, errLeaseInvalid
	default:
	}
	return l.current, nil
}

func (l *uiLeases) valid(lease *uiLease) bool {
	_, err := l.view(lease)
	if err != nil {
		return false
	}
	select {
	case <-lease.done:
		return false
	default:
		return true
	}
}

func (l *uiLeases) snapshot(id uuid.UUID, feed *eventFeed) (contract.Snapshot, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expireLocked()
	if l.closed || l.current == nil || l.current.id != id {
		return contract.Snapshot{}, errLeaseInvalid
	}
	observation := feed.snapshot()
	l.expireLocked()
	if l.closed || l.current == nil || l.current.id != id {
		return contract.Snapshot{}, errLeaseInvalid
	}
	select {
	case <-l.current.done:
		return contract.Snapshot{}, errLeaseInvalid
	default:
	}
	state := observation.state
	return contract.Snapshot{
		ApiVersion: "v1", BackendRunId: state.BackendRunID, Revision: state.Revision,
		LastEventSequence: observation.sequence, ActiveTest: state.Record,
		PendingAction: state.PendingAction, Measurements: state.Measurements,
	}, nil
}

func (l *uiLeases) subscribe(owner *uiLease, feed *eventFeed) (*eventSubscription, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expireLocked()
	if l.closed || l.current != owner {
		return nil, errLeaseInvalid
	}
	select {
	case <-owner.done:
		return nil, errLeaseInvalid
	default:
	}
	sub := feed.subscribe()
	select {
	case <-owner.done:
		feed.unsubscribe(sub)
		return nil, errLeaseInvalid
	default:
	}
	return sub, nil
}

func (l *uiLeases) expireLocked() {
	if l.current != nil && !time.Now().Before(l.current.expiresAt) {
		l.revokeLocked("ui_lost")
	}
}

func (l *uiLeases) revokeLocked(reason string) {
	lease := l.current
	if lease == nil {
		return
	}
	l.current = nil
	lease.controller.Revoke(reason)
}

func (l *uiLeases) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	l.revokeLocked("owner_lost")
}
