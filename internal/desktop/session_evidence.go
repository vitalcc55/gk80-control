package desktop

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/transport"
)

// sessionEvidence holds control confirmations and their time budget. The session mutex protects it.
type sessionEvidence struct {
	leaseID        uuid.UUID
	leaseUntil     time.Time
	leaseConfirmed bool
	lastUI         time.Time
	lastProbe      time.Time
	liveMark       time.Time
	liveDuration   time.Duration
	recoverySince  time.Time
	unknownAcquire bool
}

func (s *sessionEvidence) deadline() time.Time {
	var earliest time.Time
	for _, limit := range []time.Time{s.lastUI.Add(uiHeartbeatFreshness), s.lastProbe.Add(streamProbeFreshness), s.leaseUntil} {
		if !limit.IsZero() && (earliest.IsZero() || limit.Before(earliest)) {
			earliest = limit
		}
	}
	return earliest
}
func (s *sessionEvidence) freshness(now time.Time, unavailable bool, state *sessionStreamState) sessionFreshness {
	result := sessionFreshness{status: contract.Disconnected}
	if unavailable || s.unknownAcquire || state.streamBroken || s.leaseID == uuid.Nil || !s.leaseConfirmed || s.lastUI.IsZero() || now.Sub(s.lastUI) >= uiHeartbeatFreshness || !now.Before(s.leaseUntil) {
		return result
	}
	result.status = contract.Recovering
	if !state.streamActive || state.streamDone == nil || state.stream == nil || s.lastProbe.IsZero() || now.Sub(s.lastProbe) >= streamProbeFreshness {
		return result
	}
	pending := state.stream.Pending()
	select {
	case <-state.streamDone:
		return result
	default:
	}
	result.linkValid = true
	result.deadline = s.deadline()
	if !pending && !state.snapshotPending && state.snapshot != nil {
		result.status = contract.Live
	}
	return result
}
func (s *sessionEvidence) pauseLive(now, pendingSince time.Time) {
	if s.liveMark.IsZero() {
		return
	}
	end := now
	if deadline := s.deadline(); !deadline.IsZero() && deadline.Before(end) {
		end = deadline
	}
	if !pendingSince.IsZero() && pendingSince.Before(end) {
		end = pendingSince
	}
	if end.After(s.liveMark) {
		s.liveDuration += end.Sub(s.liveMark)
	}
	s.liveMark = time.Time{}
	if s.liveDuration >= streamProbeFreshness {
		s.recoverySince = time.Time{}
		s.liveDuration = 0
	}
}

// keepAlive is triggered by the mounted UI, not by a host timer or WebSocket.
func (s *demoSession) keepAlive(ctx context.Context) error {
	return s.keepAliveUntil(ctx, time.Time{})
}

func (s *demoSession) keepAliveUntil(ctx context.Context, admissionDeadline time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed || s.closing || ctx.Err() != nil || (!admissionDeadline.IsZero() && !time.Now().Before(admissionDeadline)) {
		s.mu.Unlock()
		return errSessionUnavailable
	}
	if s.unknownAcquire || (s.streamBroken && (s.cleanupUnknown || s.streamActive || s.leaseID == uuid.Nil)) {
		s.mu.Unlock()
		return errSessionUnavailable
	}
	s.mu.Unlock()
	if !s.leaseGate.TryLock() {
		return errSessionBusy
	}
	defer s.leaseGate.Unlock()
	s.mu.Lock()
	started := time.Now()
	if s.closed || s.closing || ctx.Err() != nil || (!admissionDeadline.IsZero() && !started.Before(admissionDeadline)) || s.unknownAcquire || (s.streamBroken && (s.cleanupUnknown || s.streamActive || s.leaseID == uuid.Nil)) {
		s.mu.Unlock()
		return errSessionUnavailable
	}
	s.pauseLiveLocked(started)
	previousLease := s.leaseID
	previousUntil := s.leaseUntil
	continuous := s.leaseConfirmed && previousLease != uuid.Nil && started.Before(previousUntil) && !s.lastUI.IsZero() && started.Before(s.lastUI.Add(uiHeartbeatFreshness))
	s.lastUI = started
	if !s.recoverySince.IsZero() && started.Sub(s.recoverySince) >= sessionRecoveryLimit {
		s.streamBroken = true
	}
	if s.streamBroken && (s.cleanupUnknown || s.streamActive || s.leaseID == uuid.Nil) {
		s.mu.Unlock()
		return errSessionUnavailable
	}
	lease := s.leaseID
	broken := s.streamBroken
	requestCtx, cancel := context.WithDeadline(ctx, started.Add(sessionRequestLimit))
	s.mu.Unlock()
	stop := context.AfterFunc(s.ctx, cancel)
	defer func() { stop(); cancel() }()
	if err := requestCtx.Err(); err != nil {
		return err
	}
	var granted contract.UiLease
	var err error
	if broken {
		_, err = s.client.AcquireLease(requestCtx, s.clientID, &lease)
		s.mu.Lock()
		defer s.mu.Unlock()
		var failure *transport.RequestError
		if errors.As(err, &failure) && failure.Status == 403 && failure.Code == "lease_invalid" && !s.closed && !s.closing && s.leaseID == lease {
			s.leaseID = uuid.Nil
			s.leaseUntil = time.Time{}
			s.leaseConfirmed = false
			s.streamBroken = false
			s.recoverySince = time.Time{}
			s.liveDuration = 0
			s.changedLocked()
		}
		return errSessionUnavailable
	}
	if lease == uuid.Nil {
		granted, err = s.client.AcquireLease(requestCtx, s.clientID, nil)
	} else {
		granted, err = s.client.RenewLease(requestCtx, lease)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.closing {
		return errSessionUnavailable
	}
	if err != nil {
		var failure *transport.RequestError
		if lease == uuid.Nil && errors.As(err, &failure) && failure.OutcomeUnknown {
			s.unknownAcquire = true
		}
		if lease != uuid.Nil && errors.As(err, &failure) && failure.Status == 403 && failure.Code == "lease_invalid" {
			s.leaseID = uuid.Nil
			s.leaseUntil = time.Time{}
		}
		s.lastProbe = time.Time{}
		s.leaseConfirmed = false
		s.invalidateActionsLocked()
		s.changedLocked()
		_ = s.connectionStateLocked(time.Now())
		return err
	}
	if granted.LeaseId == uuid.Nil || granted.ClientId != s.clientID || (lease != uuid.Nil && granted.LeaseId != lease) {
		if lease == uuid.Nil {
			s.unknownAcquire = true
		}
		s.lastProbe = time.Time{}
		s.leaseConfirmed = false
		s.invalidateActionsLocked()
		s.changedLocked()
		_ = s.connectionStateLocked(time.Now())
		return errSessionUnavailable
	}
	s.leaseID = granted.LeaseId
	s.leaseConfirmed = true
	s.leaseUntil = started.Add(leaseDuration)
	if granted.ExpiresAt.Before(s.leaseUntil) {
		s.leaseUntil = granted.ExpiresAt
	}
	if !continuous || previousLease != granted.LeaseId || s.leaseUntil.Before(previousUntil) {
		s.changedLocked()
	}
	if err := requestCtx.Err(); err != nil {
		s.leaseConfirmed = false
		s.lastProbe = time.Time{}
		s.invalidateActionsLocked()
		s.changedLocked()
		_ = s.connectionStateLocked(time.Now())
		return err
	}
	if time.Since(s.lastUI) >= uiHeartbeatFreshness || !time.Now().Before(s.leaseUntil) {
		return errSessionUnavailable
	}
	if !s.streamActive {
		s.beginStream()
		if s.recoverySince.IsZero() {
			s.recoverySince = time.Now()
		}
		go s.runStream(granted.LeaseId)
	}
	s.startLiveLocked(time.Now())
	return nil
}

func (s *demoSession) currentLease() (uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.leaseConfirmed || s.leaseID == uuid.Nil || !time.Now().Before(s.leaseUntil) {
		return uuid.Nil, errSessionUnavailable
	}
	return s.leaseID, nil
}

func (s *demoSession) invalidateActionsLocked() {
	s.cancelActions()
	if !s.closed && !s.closing {
		s.actionsCtx, s.cancelActions = context.WithCancel(s.ctx)
	}
}

func (s *demoSession) startLiveLocked(now time.Time) {
	if s.liveMark.IsZero() && s.freshnessLocked(now).status == contract.Live {
		s.liveMark = now
	}
}
