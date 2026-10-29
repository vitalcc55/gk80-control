package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/demo"
	"github.com/vitalcc55/gk80-control/internal/storage"
)

func TestLeaseOwnershipRecoveryAndRelease(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	engine, err := demo.NewEngine(context.Background(), store, demo.NewSimulator(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	leases := newUILeases(engine, time.Second)
	defer leases.close()
	client := uuid.New()
	first, err := leases.acquire(client, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leases.acquire(client, uuid.Nil); !errors.Is(err, errLeaseBusy) {
		t.Fatalf("client id alone restored lease: %v", err)
	}
	if _, err := leases.acquire(uuid.New(), first.id); !errors.Is(err, errLeaseBusy) {
		t.Fatalf("other client: %v", err)
	}
	recovered, err := leases.acquire(client, first.id)
	if err != nil || recovered != first {
		t.Fatalf("recovery: %v", err)
	}
	if _, err := leases.renew(uuid.New()); !errors.Is(err, errLeaseInvalid) {
		t.Fatalf("wrong lease: %v", err)
	}
	if err := leases.release(first.id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.done:
	default:
		t.Fatal("release did not revoke lease")
	}
	if _, err := leases.renew(first.id); !errors.Is(err, errLeaseInvalid) {
		t.Fatalf("released lease: %v", err)
	}
	if _, err := leases.acquire(uuid.New(), uuid.Nil); err != nil {
		t.Fatalf("next owner: %v", err)
	}
}

func TestLeaseExpiryAndStaleTimerAfterRenewal(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	engine, err := demo.NewEngine(context.Background(), store, demo.NewSimulator(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	leases := newUILeases(engine, time.Minute)
	defer leases.close()
	lease, err := leases.acquire(uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leases.renew(lease.id); err != nil {
		t.Fatal(err)
	}
	leases.mu.Lock()
	leases.expireLocked()
	leases.mu.Unlock()
	select {
	case <-lease.done:
		t.Fatal("old timer revoked renewed lease")
	default:
	}
	leases.mu.Lock()
	lease.expiresAt = time.Now().Add(-time.Second)
	leases.expireLocked()
	leases.mu.Unlock()
	select {
	case <-lease.done:
	default:
		t.Fatal("expired lease was not revoked")
	}
	if _, err := leases.renew(lease.id); !errors.Is(err, errLeaseInvalid) {
		t.Fatalf("expired lease: %v", err)
	}
}

func TestLeaseTimerRevokesWithoutAnotherRequest(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	engine, err := demo.NewEngine(context.Background(), store, demo.NewSimulator(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	leases := newUILeases(engine, 20*time.Millisecond)
	defer leases.close()
	lease, err := leases.acquire(uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-lease.done:
	case <-time.After(5 * time.Second):
		t.Fatal("lease did not expire")
	}
}

func TestControllerRejectsExpiredLeaseBeforeTimer(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	engine, err := demo.NewEngine(context.Background(), store, demo.NewSimulator(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	leases := newUILeases(engine, time.Minute)
	defer leases.close()
	lease, err := leases.acquire(uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lease.controller.Renew(-time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := lease.controller.Stop(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, demo.ErrControlRevoked) {
		t.Fatalf("expired controller accepted stop: %v", err)
	}
}
