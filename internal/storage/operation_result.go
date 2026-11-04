package storage

import (
	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

// SavedState is a confirmed record and its corresponding database revision.
// It does not describe newer operational equipment state held by Engine.
type SavedState struct {
	Record        contract.TestRecord
	Revision      int64
	PendingAction *contract.PendingAction
}

// OperationResult separates a known command from a newly applied transition.
// Nil Saved means no current saved state is confirmed by this operation; zero
// is a valid revision. Replay never supplies a historical transition snapshot.
type OperationResult struct {
	Command  *contract.CommandStatus
	Saved    *SavedState
	Applied  bool
	Replayed bool
}

// CommandStatus projects the existing external status without extending its DTO.
func (r OperationResult) CommandStatus() contract.CommandStatus {
	if r.Command == nil {
		return contract.CommandStatus{}
	}
	return *r.Command
}

func operatorResult(status contract.CommandStatus, prepared *SavedState, attempted bool, err error) (OperationResult, error) {
	result := OperationResult{}
	if status.CommandId != uuid.Nil {
		result.Command = &status
		result.Replayed = !attempted
	}
	if err == nil && prepared != nil {
		result.Saved = prepared
		result.Applied = true
	}
	return result, err
}
