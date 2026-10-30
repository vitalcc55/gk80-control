package demo

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/storage"
)

type observedControlContext struct {
	context.Context
	once    sync.Once
	checked chan struct{}
}

func (c *observedControlContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.checked) })
	return err
}

func TestCancelledControlAdmissionDoesNotMutateEngine(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	engine := newTestEngine(t, store, NewSimulator(), uuid.New())
	ctx, cancel := context.WithCancel(context.Background())
	observed := &observedControlContext{Context: ctx, checked: make(chan struct{})}
	engine.mu.Lock()
	done := make(chan error, 1)
	go func() { _, _, err := engine.GrantTimedControl(observed, time.Minute); done <- err }()
	select {
	case <-observed.checked:
	case <-time.After(5 * time.Second):
		engine.mu.Unlock()
		t.Fatal("grant did not check context")
	}
	cancel()
	engine.mu.Unlock()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled grant: %v", err)
	}
	engine.mu.Lock()
	if engine.controller != nil {
		engine.mu.Unlock()
		t.Fatal("cancelled grant published controller")
	}
	engine.mu.Unlock()
	control, before, err := engine.GrantTimedControl(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Revoke("ui_lost")
	ctx, cancel = context.WithCancel(context.Background())
	observed = &observedControlContext{Context: ctx, checked: make(chan struct{})}
	engine.mu.Lock()
	go func() { _, err := control.RenewContext(observed, 2*time.Minute); done <- err }()
	select {
	case <-observed.checked:
	case <-time.After(5 * time.Second):
		engine.mu.Unlock()
		t.Fatal("renew did not check context")
	}
	cancel()
	engine.mu.Unlock()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled renew: %v", err)
	}
	engine.mu.Lock()
	if control.expiresAt != before || control.revoked {
		engine.mu.Unlock()
		t.Fatal("cancelled renew changed control")
	}
	engine.mu.Unlock()
	if _, err := control.RenewContext(context.Background(), time.Minute); err != nil {
		t.Fatalf("next renew: %v", err)
	}
}
