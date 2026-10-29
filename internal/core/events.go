package core

import (
	"context"
	"errors"
	"sync"

	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/demo"
)

const eventQueueLimit = 64

var errSubscriptionClosed = errors.New("event subscription closed")

type eventSource interface {
	State() demo.State
	Changes() <-chan struct{}
}

type eventSnapshot struct {
	state    demo.State
	sequence int64
}

type eventSubscription struct {
	events chan contract.Event
	done   chan struct{}
}

// eventFeed uses Changes as an invalidation signal, not as a log of transitions.
type eventFeed struct {
	mu       sync.Mutex
	source   eventSource
	sequence int64
	current  *eventSubscription
	closed   bool
}

func newEventFeed(source eventSource) *eventFeed { return &eventFeed{source: source} }

func (f *eventFeed) run(ctx context.Context) {
	defer f.close()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-f.source.Changes():
			if !ok {
				return
			}
			f.publish()
		}
	}
}

func (f *eventFeed) snapshot() eventSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return eventSnapshot{state: f.source.State(), sequence: f.sequence}
}

func (f *eventFeed) subscribe() *eventSubscription {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detachLocked()
	sub := &eventSubscription{events: make(chan contract.Event, eventQueueLimit), done: make(chan struct{})}
	if f.closed {
		close(sub.done)
	} else {
		f.current = sub
	}
	return sub
}

func (f *eventFeed) unsubscribe(sub *eventSubscription) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.current == sub {
		f.detachLocked()
	}
}

// next never returns a queued event after this subscription has been detached.
func (f *eventFeed) next(ctx context.Context, sub *eventSubscription) (contract.Event, error) {
	select {
	case <-ctx.Done():
		return contract.Event{}, ctx.Err()
	case <-sub.done:
		return contract.Event{}, errSubscriptionClosed
	case event := <-sub.events:
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.closed || f.current != sub {
			return contract.Event{}, errSubscriptionClosed
		}
		return event, nil
	}
}

func (f *eventFeed) publish() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	state := f.source.State()
	f.sequence++
	if f.current == nil {
		return
	}
	event := contract.Event{BackendRunId: state.BackendRunID, Revision: state.Revision, Sequence: f.sequence, Type: "stateChanged"}
	select {
	case f.current.events <- event:
	default:
		f.detachLocked()
	}
}

func (f *eventFeed) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.detachLocked()
}

func (f *eventFeed) detachLocked() {
	if f.current != nil {
		close(f.current.done)
		f.current = nil
	}
}
