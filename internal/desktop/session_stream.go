package desktop

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/transport"
)

// sessionStreamState is the current stream and snapshot lifecycle, protected by the session mutex.
// runStream alone closes streamStopped; each consumeStream owns its probe worker.
type sessionStreamState struct {
	snapshot         *contract.Snapshot
	snapshotPending  bool
	streamActive     bool
	streamGeneration int64
	streamDone       <-chan struct{}
	stream           sessionStream
	streamStopped    chan struct{}
	streamBroken     bool
	cleanupUnknown   bool
}

// These transitions run under demoSession.mu. The coordinator publishes only
// after updating the associated lease/probe evidence in the same critical section.
func (s *sessionStreamState) beginStream() {
	s.streamActive = true
	s.snapshotPending = true
	s.streamStopped = make(chan struct{})
}

func (s *sessionStreamState) attachStream(stream sessionStream) {
	s.stream = stream
	s.streamDone = stream.Done()
}

func (s *sessionStreamState) detachStream() {
	s.streamDone = nil
	s.stream = nil
}

func (s *sessionStreamState) finishStream() {
	s.streamActive = false
	s.detachStream()
	s.snapshotPending = true
}

func (s *sessionStreamState) failStreamCleanup() {
	s.streamBroken = true
	s.cleanupUnknown = true
}

func (s *sessionStreamState) streamGap() { s.streamGeneration++ }

func (s *sessionStreamState) requireSnapshot() { s.snapshotPending = true }

func (s *sessionStreamState) snapshotSynchronized() { s.snapshotPending = false }

func (s *demoSession) canObserve(lease uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	return !s.closed && !s.closing && !s.streamBroken && s.leaseConfirmed && s.leaseID == lease && now.Before(s.leaseUntil) && !s.lastUI.IsZero() && now.Sub(s.lastUI) < uiHeartbeatFreshness
}

func (s *demoSession) runStream(lease uuid.UUID) {
	defer func() {
		s.mu.Lock()
		s.finishStream()
		s.lastProbe = time.Time{}
		s.changedLocked()
		close(s.streamStopped)
		s.mu.Unlock()
	}()
	for attempt := 0; attempt < 3 && s.canObserve(lease); attempt++ {
		streamCtx, cancel := context.WithCancel(s.ctx)
		timer := time.AfterFunc(sessionRequestLimit, cancel)
		stream, snapshot, err := s.client.openStream(streamCtx, lease, s.backendID)
		started := timer.Stop()
		if err == nil && (!started || streamCtx.Err() != nil) {
			err = errSessionUnavailable
		}
		if err == nil {
			s.mu.Lock()
			s.attachStream(stream)
			s.mu.Unlock()
			err = s.consumeStream(streamCtx, lease, stream, snapshot)
		}
		cancel()
		if stream != nil {
			s.mu.Lock()
			s.streamGap()
			s.pauseLiveLocked(time.Now())
			s.requireSnapshot()
			s.lastProbe = time.Time{}
			s.changedLocked()
			_ = s.connectionStateLocked(time.Now())
			s.mu.Unlock()
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 4*time.Second)
			closeErr := stream.Close(closeCtx)
			closeCancel()
			if closeErr != nil {
				s.mu.Lock()
				s.failStreamCleanup()
				s.mu.Unlock()
				return
			}
		}
		s.mu.Lock()
		s.pauseLiveLocked(time.Now())
		stable := s.recoverySince.IsZero()
		if s.recoverySince.IsZero() {
			s.recoverySince = time.Now()
		}
		s.detachStream()
		s.lastProbe = time.Time{}
		s.changedLocked()
		s.mu.Unlock()
		if stable {
			attempt = -1
		}
		if errors.Is(err, transport.ErrStreamCloseUnconfirmed) {
			s.mu.Lock()
			s.failStreamCleanup()
			s.mu.Unlock()
			return
		}
		if !s.canObserve(lease) {
			return
		}
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
	if s.canObserve(lease) {
		s.mu.Lock()
		s.streamBroken = true
		s.changedLocked()
		s.mu.Unlock()
	}
}

func (s *demoSession) consumeStream(ctx context.Context, lease uuid.UUID, stream sessionStream, snapshot contract.Snapshot) (result error) {
	syncCtx, syncCancel := context.WithTimeout(ctx, sessionSyncLimit)
	err := s.acceptSnapshot(syncCtx, lease, snapshot)
	if err == nil {
		err = s.drainStream(syncCtx, lease, stream)
	}
	syncCancel()
	if err != nil {
		return err
	}
	workerCtx, cancelWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	probeError := make(chan error, 1)
	go func() {
		defer close(workerDone)
		for workerCtx.Err() == nil {
			probeAt := time.Now()
			probeCtx, cancel := context.WithTimeout(workerCtx, sessionSyncLimit)
			err := stream.Probe(probeCtx)
			cancel()
			if err != nil {
				if workerCtx.Err() == nil {
					s.mu.Lock()
					if s.stream == stream && s.leaseID == lease {
						s.lastProbe = time.Time{}
						s.requireSnapshot()
						s.changedLocked()
						_ = s.connectionStateLocked(time.Now())
					}
					s.mu.Unlock()
					probeError <- err
					cancelWorker()
				}
				return
			}
			s.mu.Lock()
			valid := !s.closed && !s.closing && s.stream == stream && s.leaseID == lease && workerCtx.Err() == nil
			select {
			case <-stream.Done():
				valid = false
			default:
			}
			if valid {
				s.lastProbe = probeAt
				now := time.Now()
				freshness := s.freshnessLocked(now)
				if s.liveMark.IsZero() && freshness.status == contract.Live {
					s.liveMark = now
				}
				_ = s.publishConnectionLocked(now, freshness)
			}
			s.mu.Unlock()
			if !valid {
				return
			}
			wait := time.Until(probeAt.Add(streamProbeInterval))
			if wait < 0 {
				wait = 0
			}
			timer := time.NewTimer(wait)
			select {
			case <-workerCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	defer func() {
		cancelWorker()
		select {
		case <-workerDone:
		case <-time.After(4 * time.Second):
			result = transport.ErrStreamCloseUnconfirmed
		}
	}()
	for s.canObserve(lease) {
		nextCtx, cancel := context.WithTimeout(workerCtx, time.Second)
		event, err := stream.Next(nextCtx)
		cancel()
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			s.connectionState()
			continue
		}
		if err != nil {
			select {
			case failure := <-probeError:
				return failure
			default:
			}
			return err
		}
		s.mu.Lock()
		s.pauseLiveLocked(time.Now())
		s.requireSnapshot()
		if s.onEvent != nil {
			s.onEvent(event)
		}
		s.changedLocked()
		s.mu.Unlock()
		syncCtx, syncCancel := context.WithTimeout(workerCtx, sessionSyncLimit)
		snapshot, err := readSessionSnapshot(syncCtx, stream)
		if err != nil {
			syncCancel()
			return err
		}
		if err := s.acceptSnapshot(syncCtx, lease, snapshot); err != nil {
			syncCancel()
			return err
		}
		err = s.drainStream(syncCtx, lease, stream)
		syncCancel()
		if err != nil {
			return err
		}
	}
	return errSessionUnavailable
}

func (s *demoSession) drainStream(ctx context.Context, lease uuid.UUID, stream sessionStream) error {
	s.mu.Lock()
	s.pauseLiveLocked(time.Now())
	s.requireSnapshot()
	s.changedLocked()
	s.mu.Unlock()
	for {
		event, present, err := stream.Poll(ctx)
		if err != nil {
			return err
		}
		if !present {
			s.mu.Lock()
			s.snapshotSynchronized()
			s.startLiveLocked(time.Now())
			s.changedLocked()
			s.mu.Unlock()
			return nil
		}
		s.mu.Lock()
		if s.onEvent != nil {
			s.onEvent(event)
		}
		s.changedLocked()
		s.mu.Unlock()
		snapshot, err := readSessionSnapshot(ctx, stream)
		if err != nil {
			return err
		}
		if err := s.acceptSnapshot(ctx, lease, snapshot); err != nil {
			return err
		}
	}
}

func (s *demoSession) wait(ctx context.Context) error {
	s.mu.Lock()
	stopped := s.streamStopped
	s.mu.Unlock()
	if stopped == nil {
		return nil
	}
	select {
	case <-stopped:
		s.mu.Lock()
		unknown := s.cleanupUnknown
		s.mu.Unlock()
		if unknown {
			return ErrSessionShutdownUnconfirmed
		}
		return nil
	case <-ctx.Done():
		return ErrSessionShutdownUnconfirmed
	}
}

func readSessionSnapshot(ctx context.Context, stream sessionStream) (contract.Snapshot, error) {
	for {
		snapshot, err := stream.Snapshot(ctx)
		var failure *transport.RequestError
		if err == nil || !errors.As(err, &failure) || failure.Status != 503 || failure.Code != "state_unavailable" {
			return snapshot, err
		}
		select {
		case <-ctx.Done():
			return contract.Snapshot{}, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
