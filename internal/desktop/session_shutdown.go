package desktop

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

// shutdown preserves the ordering of evidence gathering. Stop is sent before
// reads that may block; command outcome, saved record and cleanup are separate.
func (s *demoSession) shutdown(ctx context.Context) error {
	s.mu.Lock()
	if !s.closed {
		s.closing = true
		s.cancelActions()
		s.changedLocked()
		_ = s.connectionStateLocked(time.Now())
	}
	leasePending := !s.leaseGate.TryLock()
	if !leasePending {
		s.leaseGate.Unlock()
	}
	lease := s.leaseID
	unknownAcquire := s.unknownAcquire
	active, ledgerUnconfirmed := s.shutdownEvidence(s.snapshot, s.backendID)
	unconfirmed := unknownAcquire || leasePending || s.cleanupUnknown || ledgerUnconfirmed
	closed := s.closed
	s.mu.Unlock()
	if closed || lease == uuid.Nil {
		if unconfirmed {
			return ErrSessionShutdownUnconfirmed
		}
		return nil
	}
	if active != nil && active.Stage != "finished" {
		s.startShutdownStop(ctx, lease, active)
	}
	fresh, snapshotErr := s.client.Snapshot(ctx, lease)
	snapshotKnown := snapshotErr == nil && fresh.BackendRunId == s.backendID
	if snapshotKnown {
		s.mu.Lock()
		active = s.savedRecordLocked(fresh.ActiveTest)
		s.mu.Unlock()
		if active != nil && active.Stage != "finished" {
			s.startShutdownStop(ctx, lease, active)
		}
	}
	unknown := unknownAcquire || leasePending
	s.mu.Lock()
	pending, terminalUnknown := s.shutdownPending(active)
	unknown = unknown || terminalUnknown
	s.mu.Unlock()
	confirmFinishedStop := false
	var startsAfterSave []uuid.UUID
	for _, id := range pending {
		s.mu.Lock()
		attempt, unresolved := s.shutdownAttempt(id)
		s.mu.Unlock()
		if unresolved {
			stopForActive := active != nil && id == active.StopCommandId
			if stopForActive && active.Stage == "finished" {
				stopIdentity, err := commandIdentity("stop:"+active.TestId.String(), contract.StopTestCommand{CommandId: id})
				if err == nil && attempt.identity == stopIdentity {
					confirmFinishedStop = true
				} else {
					unknown = true
				}
			} else if !stopForActive {
				unknown = true
			}
			continue
		}
		var status contract.CommandStatus
		var err error
		if attempt.isStop() {
			s.mu.Lock()
			tracked := s.commandEvidence(id)
			s.mu.Unlock()
			if tracked.TestId == nil {
				err = errSessionUnavailable
			} else {
				status, err = s.lookupStop(ctx, lease, *tracked.TestId, id)
			}
		} else {
			status, err = s.client.Command(ctx, lease, id)
		}
		s.mu.Lock()
		status, err = s.applyShutdownObservation(attempt, status, err)
		s.mu.Unlock()
		stopForActive := active != nil && id == active.StopCommandId
		if stopForActive && active.Stage == "finished" {
			if err != nil || status.Status != contract.CommandStatusStatusCompleted {
				if active.SaveStatus != "saved" {
					confirmFinishedStop = true
				} else {
					unknown = true
				}
			}
		} else if (err != nil || !terminalCommand(status.Status)) && !stopForActive {
			if active != nil && (active.Stage != "finished" || active.SaveStatus != "saved") && attempt.startsTest(active.TestId) {
				startsAfterSave = append(startsAfterSave, id)
			} else {
				unknown = true
			}
		}
	}
	s.mu.Lock()
	if !s.allCreatesCovered(&fresh, s.backendID) {
		unknown = true
	}
	s.mu.Unlock()
	var result error
	s.mu.Lock()
	cleanupUnknown := s.cleanupUnknown || s.hasDisplaced()
	s.mu.Unlock()
	if unknown || !snapshotKnown || cleanupUnknown {
		result = ErrSessionShutdownUnconfirmed
	}
	if active != nil && (active.Stage != "finished" || confirmFinishedStop) {
		stopID := active.StopCommandId
		stopIdentity, identityErr := commandIdentity("stop:"+active.TestId.String(), contract.StopTestCommand{CommandId: stopID})
		if identityErr != nil {
			return ErrSessionShutdownUnconfirmed
		}
		if active.Stage != "finished" {
			s.startShutdownStop(ctx, lease, active)
		}
		for {
			s.mu.Lock()
			cached, tracked := s.exactCommandEvidence(stopID, stopIdentity)
			s.mu.Unlock()
			if tracked && cached.Status == contract.CommandStatusStatusCompleted {
				break
			}
			status, err := s.lookupStop(ctx, lease, active.TestId, stopID)
			if err == nil && status.Status == contract.CommandStatusStatusCompleted {
				break
			}
			if err == nil && (status.Status == contract.CommandStatusStatusFailed || status.Status == contract.CommandStatusStatusRejected) {
				result = ErrSessionShutdownUnconfirmed
				break
			}
			select {
			case <-ctx.Done():
				return ErrSessionShutdownUnconfirmed
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	if active != nil && (active.Stage != "finished" || active.SaveStatus != "saved") {
		for {
			record, err := s.client.Test(ctx, lease, active.TestId)
			if err == nil && record.Stage == "finished" && record.SaveStatus == "saved" {
				break
			}
			select {
			case <-ctx.Done():
				return ErrSessionShutdownUnconfirmed
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	for _, id := range startsAfterSave {
		status, err := s.client.Command(ctx, lease, id)
		s.mu.Lock()
		status, err = s.applyStartAfterSave(id, status, err)
		s.mu.Unlock()
		if err != nil || !terminalCommand(status.Status) || status.TestId == nil || *status.TestId != active.TestId {
			result = ErrSessionShutdownUnconfirmed
		}
	}
	if err := s.client.ReleaseLease(ctx, lease); err != nil {
		result = ErrSessionShutdownUnconfirmed
	}
	return result
}

func (s *demoSession) startShutdownStop(ctx context.Context, lease uuid.UUID, active *contract.TestRecord) {
	stopID := active.StopCommandId
	command := contract.StopTestCommand{CommandId: stopID}
	identity, err := commandIdentity("stop:"+active.TestId.String(), command)
	if err != nil {
		return
	}
	s.mu.Lock()
	tracked := s.registerShutdownStop(active, identity)
	s.mu.Unlock()
	if tracked {
		return
	}
	status, err := s.client.StopTest(ctx, lease, active.TestId, command)
	s.mu.Lock()
	s.finishShutdownStop(stopID, identity, status, err)
	s.mu.Unlock()
}
