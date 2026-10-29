package core

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/demo"
)

type eventTestSource struct {
	mu       sync.Mutex
	state    demo.State
	changed  chan struct{}
	captured chan struct{}
	release  chan struct{}
}

func (s *eventTestSource) State() demo.State {
	s.mu.Lock()
	state, captured, release := s.state, s.captured, s.release
	s.captured, s.release = nil, nil
	s.mu.Unlock()
	if captured != nil {
		close(captured)
		<-release
	}
	return state
}

func (s *eventTestSource) Changes() <-chan struct{} { return s.changed }
func (s *eventTestSource) update(revision int64) {
	s.mu.Lock()
	s.state.Revision = revision
	s.mu.Unlock()
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func receiveEvent(t *testing.T, feed *eventFeed, sub *eventSubscription) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	event, err := feed.next(ctx, sub)
	if err != nil || event.Type != "stateChanged" || event.Sequence < 1 {
		t.Fatalf("event: %+v %v", event, err)
	}
	return event.Sequence
}

func TestEventSnapshotBoundaryRetainsConcurrentChange(t *testing.T) {
	source := &eventTestSource{state: demo.State{BackendRunID: uuid.New()}, changed: make(chan struct{}, 1)}
	feed := newEventFeed(source)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go feed.run(ctx)
	sub := feed.subscribe()
	defer feed.unsubscribe(sub)
	source.mu.Lock()
	source.captured, source.release = make(chan struct{}), make(chan struct{})
	captured, release := source.captured, source.release
	source.mu.Unlock()
	snapshots := make(chan eventSnapshot, 1)
	go func() { snapshots <- feed.snapshot() }()
	select {
	case <-captured:
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot did not read state")
	}
	source.update(1)
	close(release)
	snapshot := <-snapshots
	if snapshot.state.Revision != 0 || snapshot.sequence != 0 {
		t.Fatalf("snapshot: %+v", snapshot)
	}
	sequence := receiveEvent(t, feed, sub)
	if sequence != 1 {
		t.Fatalf("sequence: %d", sequence)
	}
	updated := feed.snapshot()
	if updated.state.Revision != 1 || updated.sequence != 1 {
		t.Fatalf("updated snapshot: %+v", updated)
	}
}

func TestEventOverflowAndReplacementCloseOnlyOldSubscription(t *testing.T) {
	source := &eventTestSource{state: demo.State{BackendRunID: uuid.New()}, changed: make(chan struct{}, 1)}
	feed := newEventFeed(source)
	old := feed.subscribe()
	feed.publish()
	current := feed.subscribe()
	select {
	case <-old.done:
	default:
		t.Fatal("old subscriber was not closed")
	}
	feed.unsubscribe(old)
	if _, err := feed.next(context.Background(), old); !errors.Is(err, errSubscriptionClosed) {
		t.Fatalf("old queued event delivered: %v", err)
	}
	for i := 0; i <= eventQueueLimit; i++ {
		feed.publish()
	}
	select {
	case <-current.done:
	default:
		t.Fatal("overflow did not close subscription")
	}
	if _, err := feed.next(context.Background(), current); !errors.Is(err, errSubscriptionClosed) {
		t.Fatalf("overflow queue delivered: %v", err)
	}
	next := feed.subscribe()
	defer feed.unsubscribe(next)
	feed.publish()
	if sequence := receiveEvent(t, feed, next); sequence != int64(eventQueueLimit+3) {
		t.Fatalf("sequence after overflow: %d", sequence)
	}
}

func TestEventReadRechecksOwnershipAfterConcurrentDetach(t *testing.T) {
	source := &eventTestSource{state: demo.State{BackendRunID: uuid.New()}, changed: make(chan struct{}, 1)}
	feed := newEventFeed(source)
	sub := feed.subscribe()
	feed.publish()
	feed.mu.Lock()
	result := make(chan error, 1)
	go func() { _, err := feed.next(context.Background(), sub); result <- err }()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for len(sub.events) != 0 {
		select {
		case err := <-result:
			feed.mu.Unlock()
			t.Fatalf("read returned before ownership check: %v", err)
		case <-timer.C:
			feed.mu.Unlock()
			t.Fatal("read did not take queued event")
		case <-tick.C:
		}
	}
	feed.detachLocked()
	feed.mu.Unlock()
	select {
	case err := <-result:
		if !errors.Is(err, errSubscriptionClosed) {
			t.Fatalf("detached read: %v", err)
		}
	case <-timer.C:
		t.Fatal("detached read did not finish")
	}
}

func TestEventFeedClosesOnCancellation(t *testing.T) {
	source := &eventTestSource{state: demo.State{BackendRunID: uuid.New()}, changed: make(chan struct{}, 1)}
	feed := newEventFeed(source)
	sub := feed.subscribe()
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { feed.run(ctx); close(finished) }()
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("feed did not stop")
	}
	select {
	case <-sub.done:
	default:
		t.Fatal("subscriber remained open")
	}
	if _, err := feed.next(context.Background(), feed.subscribe()); !errors.Is(err, errSubscriptionClosed) {
		t.Fatalf("subscription after close: %v", err)
	}
}
