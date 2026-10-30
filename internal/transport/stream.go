package transport

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

const nativeEventQueueLimit = 64
const nativeProbeTimeout = 2 * time.Second

var ErrStreamClosed = errors.New("core event stream closed")
var ErrStreamInvalid = errors.New("invalid core event stream")
var ErrStreamOverflow = errors.New("core event stream overflow")
var ErrStreamSequence = errors.New("core event sequence discontinuity")
var ErrStreamCloseUnconfirmed = errors.New("core event stream closure unconfirmed")

// EventStream owns one connection. Its consumer serializes Next and Snapshot.
type EventStream struct {
	client     *Client
	lease      uuid.UUID
	runID      uuid.UUID
	conn       *websocket.Conn
	ctx        context.Context
	cancel     context.CancelFunc
	queue      chan contract.Event
	done       chan struct{}
	readerDone chan struct{}
	watchDone  chan struct{}
	mu         sync.Mutex
	probeMu    sync.Mutex
	terminal   error
	consumer   chan struct{}
	cursor     int64
}

// OpenStream subscribes before reading a snapshot; ctx controls the stream's lifetime.
// The caller owns the returned stream and must close it.
func (c *Client) OpenStream(ctx context.Context, lease, expectedRunID uuid.UUID) (*EventStream, contract.Snapshot, error) {
	var empty contract.Snapshot
	if lease == uuid.Nil || expectedRunID == uuid.Nil {
		return nil, empty, invalidRequest()
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+c.secret)
	headers.Set("X-GK80-UI-Lease", lease.String())
	conn, _, err := websocket.Dial(ctx, c.endpoint+"/v1/events", &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: headers})
	if err != nil {
		return nil, empty, ErrStreamClosed
	}
	conn.SetReadLimit(contract.MaxEventBytes)
	readyCtx, readyCancel := context.WithTimeout(ctx, 3*time.Second)
	typ, data, err := conn.Read(readyCtx)
	readyCancel()
	if err != nil {
		_ = conn.CloseNow()
		if errors.Is(err, websocket.ErrMessageTooBig) {
			return nil, empty, ErrStreamInvalid
		}
		return nil, empty, ErrStreamClosed
	}
	var ready contract.StreamReady
	if typ != websocket.MessageText || validateResponse("StreamReady", data, &ready) != nil || ready.BackendRunId != expectedRunID {
		_ = conn.CloseNow()
		return nil, empty, ErrStreamInvalid
	}
	streamCtx, cancel := context.WithCancel(ctx)
	stream := &EventStream{client: c, lease: lease, runID: expectedRunID, conn: conn, ctx: streamCtx, cancel: cancel, queue: make(chan contract.Event, nativeEventQueueLimit), done: make(chan struct{}), readerDone: make(chan struct{}), watchDone: make(chan struct{}), consumer: make(chan struct{}, 1)}
	go func() { defer close(stream.watchDone); <-streamCtx.Done(); _ = stream.fail(ErrStreamClosed) }()
	go stream.read(streamCtx)
	snapshot, err := stream.Snapshot(ctx)
	if err != nil {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer closeCancel()
		if closeErr := stream.Close(closeCtx); closeErr != nil {
			return nil, empty, errors.Join(err, closeErr)
		}
		return nil, empty, err
	}
	return stream, snapshot, nil
}

func (s *EventStream) fail(err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal == nil {
		s.terminal = err
		close(s.done)
		s.cancel()
	}
	return s.terminal
}

func (s *EventStream) closeError() error {
	return s.fail(ErrStreamClosed)
}

func (s *EventStream) read(ctx context.Context) {
	defer close(s.readerDone)
	defer func() { _ = s.conn.CloseNow() }()
	var last int64
	for {
		typ, data, err := s.conn.Read(ctx)
		if err != nil {
			if errors.Is(err, websocket.ErrMessageTooBig) {
				_ = s.fail(ErrStreamInvalid)
			} else {
				_ = s.fail(ErrStreamClosed)
			}
			return
		}
		var event contract.Event
		if typ != websocket.MessageText || validateResponse("Event", data, &event) != nil || event.BackendRunId != s.runID {
			_ = s.fail(ErrStreamInvalid)
			return
		}
		if last != 0 && event.Sequence != last+1 {
			_ = s.fail(ErrStreamSequence)
			return
		}
		last = event.Sequence
		select {
		case s.queue <- event:
		case <-ctx.Done():
			_ = s.fail(ErrStreamClosed)
			return
		default:
			_ = s.fail(ErrStreamOverflow)
			return
		}
	}
}

func (s *EventStream) enter(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return s.closeError()
	case <-s.done:
		s.mu.Lock()
		err := s.terminal
		s.mu.Unlock()
		return err
	case s.consumer <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-s.consumer
			return err
		}
		return nil
	}
}

// Snapshot advances the consumer boundary; an error never makes the data live.
func (s *EventStream) Snapshot(ctx context.Context) (contract.Snapshot, error) {
	var empty contract.Snapshot
	if err := s.enter(ctx); err != nil {
		return empty, err
	}
	defer func() { <-s.consumer }()
	s.mu.Lock()
	terminal := s.terminal
	stopped := s.ctx.Err() != nil
	s.mu.Unlock()
	if terminal != nil {
		return empty, terminal
	}
	if stopped {
		return empty, s.closeError()
	}
	requestCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer func() { stop(); cancel() }()
	snapshot, err := s.client.Snapshot(requestCtx, s.lease)
	if err != nil {
		s.mu.Lock()
		terminal := s.terminal
		s.mu.Unlock()
		if terminal != nil {
			return empty, terminal
		}
		if s.ctx.Err() != nil {
			return empty, s.closeError()
		}
		return empty, err
	}
	s.mu.Lock()
	if s.terminal != nil {
		err := s.terminal
		s.mu.Unlock()
		return empty, err
	}
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return empty, s.closeError()
	}
	if ctx.Err() != nil {
		s.mu.Unlock()
		return empty, ctx.Err()
	}
	if snapshot.ApiVersion != "v1" || snapshot.BackendRunId != s.runID || snapshot.LastEventSequence < s.cursor {
		s.mu.Unlock()
		return empty, s.fail(ErrStreamInvalid)
	}
	s.cursor = snapshot.LastEventSequence
	s.mu.Unlock()
	return snapshot, nil
}

// Next discards events already covered by the latest snapshot.
func (s *EventStream) Next(ctx context.Context) (contract.Event, error) {
	if err := s.enter(ctx); err != nil {
		return contract.Event{}, err
	}
	defer func() { <-s.consumer }()
	for {
		s.mu.Lock()
		terminal := s.terminal
		stopped := s.ctx.Err() != nil
		s.mu.Unlock()
		if terminal != nil {
			return contract.Event{}, terminal
		}
		if stopped {
			return contract.Event{}, s.closeError()
		}
		if err := ctx.Err(); err != nil {
			return contract.Event{}, err
		}
		select {
		case <-ctx.Done():
			if s.ctx.Err() != nil {
				return contract.Event{}, s.closeError()
			}
			return contract.Event{}, ctx.Err()
		case <-s.ctx.Done():
			return contract.Event{}, s.closeError()
		case <-s.done:
			s.mu.Lock()
			err := s.terminal
			s.mu.Unlock()
			return contract.Event{}, err
		case event := <-s.queue:
			s.mu.Lock()
			if s.terminal != nil {
				err := s.terminal
				s.mu.Unlock()
				return contract.Event{}, err
			}
			if s.ctx.Err() != nil {
				s.mu.Unlock()
				return contract.Event{}, s.closeError()
			}
			if event.Sequence <= s.cursor {
				s.mu.Unlock()
				continue
			}
			if event.Sequence != s.cursor+1 {
				s.mu.Unlock()
				return contract.Event{}, s.fail(ErrStreamSequence)
			}
			s.cursor = event.Sequence
			s.mu.Unlock()
			return event, nil
		}
	}
}

// Probe confirms a pong on this connection; a failed probe invalidates the stream.
func (s *EventStream) Probe(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		_ = s.fail(ErrStreamClosed)
		return err
	}
	if !s.probeMu.TryLock() {
		return s.fail(ErrStreamClosed)
	}
	defer s.probeMu.Unlock()
	s.mu.Lock()
	terminal := s.terminal
	s.mu.Unlock()
	if terminal != nil {
		return terminal
	}
	probeCtx, cancel := context.WithTimeout(ctx, nativeProbeTimeout)
	stop := context.AfterFunc(s.ctx, cancel)
	defer func() { stop(); cancel() }()
	if err := s.conn.Ping(probeCtx); err != nil {
		return s.fail(ErrStreamClosed)
	}
	s.mu.Lock()
	if s.terminal != nil {
		err := s.terminal
		s.mu.Unlock()
		return err
	}
	deadline, hasDeadline := probeCtx.Deadline()
	if probeCtx.Err() != nil || (hasDeadline && !time.Now().Before(deadline)) || s.ctx.Err() != nil {
		s.mu.Unlock()
		return s.fail(ErrStreamClosed)
	}
	s.mu.Unlock()
	return nil
}

// Done closes when the connection is no longer usable.
func (s *EventStream) Done() <-chan struct{} { return s.done }

// Close requests shutdown and waits no longer than the caller's context.
func (s *EventStream) Close(ctx context.Context) error {
	_ = s.fail(ErrStreamClosed)
	select {
	case <-s.readerDone:
	case <-ctx.Done():
		return ErrStreamCloseUnconfirmed
	}
	select {
	case <-s.watchDone:
		return nil
	case <-ctx.Done():
		return ErrStreamCloseUnconfirmed
	}
}
