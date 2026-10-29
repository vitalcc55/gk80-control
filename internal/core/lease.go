package core

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
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

func (l *uiLeases) acquire(clientID, proof uuid.UUID) (*uiLease, error) {
	if clientID == uuid.Nil {
		return nil, errLeaseInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
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
	controller, err := l.engine.GrantControl()
	if err != nil {
		return nil, err
	}
	deadline, err := controller.Renew(l.duration)
	if err != nil {
		controller.Revoke("ui_lost")
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

func (l *uiLeases) renew(id uuid.UUID) (*uiLease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expireLocked()
	if l.closed || l.current == nil || id != l.current.id {
		return nil, errLeaseInvalid
	}
	deadline, err := l.current.controller.Renew(l.duration)
	if err != nil {
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

func (l *uiLeases) release(id uuid.UUID) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expireLocked()
	if l.closed || l.current == nil || id != l.current.id {
		return errLeaseInvalid
	}
	l.revokeLocked("ui_lost")
	return nil
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
