package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

func createCommand() contract.CreateTestCommand {
	return contract.CreateTestCommand{CommandId: uuid.New(), ExpectedRevision: 0, Parameters: contract.TestParameters{
		Valve: "Демо клапан", AreaM2: 0.01, PmaxPa: 1000, MeasurementSeconds: 1, Direction: "pressure", Actuator: false,
	}}
}

func TestCreatePersistsAtomicRecordAuditAndCommandAcrossReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	command := createCommand()
	status, err := store.Create(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if status.TestId == nil || status.Status != "completed" {
		t.Fatalf("unexpected status: %+v", status)
	}
	first, err := store.Test(ctx, *status.TestId)
	if err != nil {
		t.Fatal(err)
	}
	if first.Parameters != command.Parameters || !first.Demo || first.OperatorId != "demo-operator" || first.StopCommandId == uuid.Nil || first.Stage != "preparing" {
		t.Fatalf("unexpected record: %+v", first)
	}
	audit, err := store.Audit(ctx, first.TestId, 0, 100)
	if err != nil || len(audit) != 1 || audit[0].CommandId == nil || *audit[0].CommandId != command.CommandId || audit[0].Revision != 1 {
		t.Fatalf("audit: %+v, %v", audit, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	repeated, err := store.Create(ctx, command)
	if err != nil || repeated.TestId == nil || *repeated.TestId != first.TestId {
		t.Fatalf("repeat: %+v, %v", repeated, err)
	}
	reopened, err := store.Test(ctx, first.TestId)
	if err != nil || reopened != first {
		t.Fatalf("reopen: %+v, %v", reopened, err)
	}
	changed := command
	changed.Parameters.Valve = "Другой клапан"
	if _, err := store.Create(ctx, changed); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("conflict: %v", err)
	}
	changed.Parameters.Valve = ""
	if _, err := store.Create(ctx, changed); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("invalid conflicting retry: %v", err)
	}
	reserved := command
	reserved.CommandId = first.StopCommandId
	if _, err := store.Create(ctx, reserved); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("reserved id: %v", err)
	}
	stale := createCommand()
	if _, err := store.Create(ctx, stale); !errors.Is(err, ErrRevision) {
		t.Fatalf("stale: %v", err)
	}
	active := createCommand()
	active.ExpectedRevision = 1
	if _, err := store.Create(ctx, active); !errors.Is(err, ErrActiveTest) {
		t.Fatalf("second active: %v", err)
	}
}

func TestRejectedCommandKeepsItsIdentity(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	command := createCommand()
	command.ExpectedRevision = 1
	if _, err := store.Create(ctx, command); !errors.Is(err, ErrRevision) {
		t.Fatalf("first rejection: %v", err)
	}
	var operator, occurredAt string
	if err := store.db.QueryRow(`SELECT operator_id, occurred_at FROM commands WHERE id = ?`, command.CommandId.String()).Scan(&operator, &occurredAt); err != nil || operator != "demo-operator" || occurredAt == "" {
		t.Fatalf("rejected command provenance: %q %q %v", operator, occurredAt, err)
	}
	if _, err := store.Create(ctx, command); !errors.Is(err, ErrRevision) {
		t.Fatalf("repeated rejection: %v", err)
	}
	command.ExpectedRevision = 0
	if _, err := store.Create(ctx, command); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("changed rejected command: %v", err)
	}
}

func TestCancellationAfterBeginBeforeWriteDoesNotPoisonStorage(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	command := createCommand()
	request, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	wrote := false
	_, err = store.create(ctx, tx, command, request, &wrote)
	if !errors.Is(err, context.Canceled) || wrote {
		t.Fatalf("read-only cancellation: %v, write attempted %v", err, wrote)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	store.noteWriteError(ctx, err, wrote)
	if _, err := store.Create(context.Background(), createCommand()); err != nil {
		t.Fatalf("healthy storage blocked: %v", err)
	}
}

func TestCanceledTransactionBeforeFirstWriteDoesNotPoisonStorage(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	command := createCommand()
	command.Parameters.Valve = ""
	request, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	wrote := false
	_, err = store.create(ctx, tx, command, request, &wrote)
	if !errors.Is(err, context.Canceled) || wrote {
		t.Fatalf("canceled before insert: %v, write attempted %v", err, wrote)
	}
	_ = tx.Rollback()
	store.noteWriteError(ctx, sql.ErrTxDone, false)
	if _, err := store.Create(context.Background(), createCommand()); err != nil {
		t.Fatalf("healthy storage blocked: %v", err)
	}
}

func TestCanceledRequestDoesNotPoisonStorage(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Create(ctx, createCommand()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request: %v", err)
	}
	if _, err := store.Create(context.Background(), createCommand()); err != nil {
		t.Fatalf("healthy storage blocked: %v", err)
	}
}

func TestKnownCommandRemainsReadableAfterWriteFailure(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	first := createCommand()
	status, err := store.Create(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE tests SET stage = 'finished' WHERE id = ?`, status.TestId.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER reject_command BEFORE INSERT ON commands BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	second := createCommand()
	second.ExpectedRevision = 1
	if _, err := store.Create(ctx, second); err == nil {
		t.Fatal("expected write failure")
	}
	if _, err := store.Create(ctx, createCommand()); !errors.Is(err, ErrStorageUnavailable) {
		t.Fatalf("new write was not blocked: %v", err)
	}
	repeated, err := store.Create(ctx, first)
	if err != nil || repeated.TestId == nil || *repeated.TestId != *status.TestId {
		t.Fatalf("known result: %+v, %v", repeated, err)
	}
}

func TestCreateRollsBackIfAuditCannotBeWritten(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER reject_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	command := createCommand()
	if _, err := store.Create(ctx, command); err == nil {
		t.Fatal("expected write failure")
	}
	if _, err := store.Create(ctx, command); !errors.Is(err, ErrStorageUnavailable) {
		t.Fatalf("storage failure did not block another write: %v", err)
	}
	for _, table := range []string{"tests", "commands", "audit"} {
		var count int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count %d: %v", table, count, err)
		}
	}
	var revision int
	if err := store.db.QueryRowContext(ctx, "SELECT revision FROM meta WHERE id=1").Scan(&revision); err != nil || revision != 0 {
		t.Fatalf("revision %d: %v", revision, err)
	}
}

func TestConcurrentCreateDoesNotDuplicateCommand(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	command := createCommand()
	type result struct {
		status contract.CommandStatus
		err    error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			status, err := store.Create(ctx, command)
			results <- result{status, err}
		}()
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.status.TestId == nil || second.status.TestId == nil || *first.status.TestId != *second.status.TestId {
		t.Fatalf("concurrent retry: %+v, %+v", first, second)
	}
	events, err := store.Audit(ctx, *first.status.TestId, 0, 100)
	if err != nil || len(events) != 1 {
		t.Fatalf("duplicate audit: %+v, %v", events, err)
	}
}

func TestOpenRejectsUnknownDatabaseVersion(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("PRAGMA user_version = 42"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatalf("unknown schema in %s accepted", filepath.Join(dir, "demo.sqlite"))
	}
}
