package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

func createCommand() contract.CreateTestCommand {
	return contract.CreateTestCommand{CommandId: uuid.New(), ExpectedRevision: 0, Parameters: contract.TestParameters{
		Valve: "Демо клапан", AreaM2: 0.01, PmaxPa: 1000, MeasurementSeconds: 1, Direction: "pressure", Actuator: false,
	}}
}

func TestCreatePreservesExactInputIdentityAcrossReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	base := `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8","expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	var command contract.CreateTestCommand
	first, err := validator.Inspect("CreateTestCommand", []byte(base), &command)
	if err != nil || !first.Valid {
		t.Fatalf("input: %v", err)
	}
	created, err := store.CreateExact(ctx, command, first.Identity)
	if err != nil || created.TestId == nil {
		t.Fatalf("create: %+v %v", created, err)
	}
	var same contract.CreateTestCommand
	equivalent, err := validator.Inspect("CreateTestCommand", []byte(strings.Replace(base, `"areaM2":1`, `"areaM2":1.0`, 1)), &same)
	if err != nil || !equivalent.Valid {
		t.Fatal(err)
	}
	if replay, err := store.LookupCreateExact(ctx, same, equivalent.Identity); err != nil || replay.TestId == nil || *replay.TestId != *created.TestId {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	var changed contract.CreateTestCommand
	different, err := validator.Inspect("CreateTestCommand", []byte(strings.Replace(base, `"areaM2":1`, `"areaM2":1.0000000000000001`, 1)), &changed)
	if err != nil || !different.Valid || changed != command {
		t.Fatalf("different decimal: %v", err)
	}
	if _, err := store.LookupCreateExact(ctx, changed, different.Identity); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("missing conflict: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if _, err := store.CreateExact(ctx, changed, different.Identity); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("conflict after reopen: %v", err)
	}
}

func TestExactMeasurementDurationAboveLimitIsRejectedBeforeRounding(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	body := `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8","expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":3600.0000000000001,"direction":"pressure","actuator":false}}`
	var command contract.CreateTestCommand
	input, err := validator.Inspect("CreateTestCommand", []byte(body), &command)
	if err != nil || !input.Valid || command.Parameters.MeasurementSeconds != 3600 {
		t.Fatalf("inspect: %+v %v", command, err)
	}
	status, err := store.CreateExact(context.Background(), command, input.Identity)
	if !errors.Is(err, ErrInvalidCommand) || status.Status != "rejected" {
		t.Fatalf("new duration: %+v %v", status, err)
	}
	status, err = store.CreateExact(context.Background(), command, input.Identity)
	if !errors.Is(err, ErrInvalidCommand) || status.Status != "rejected" {
		t.Fatalf("rejected replay: %+v %v", status, err)
	}
}

func TestCanonicalIdentityMayExceedInputLimit(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	body := `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8","expectedRevision":0,"parameters":{"valve":"` + strings.Repeat("<", 200) + `","areaM2":1.` + strings.Repeat("0", 7400) + `1,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	if len(body) >= 8192 {
		t.Fatalf("input unexpectedly large: %d", len(body))
	}
	var command contract.CreateTestCommand
	input, err := validator.Inspect("CreateTestCommand", []byte(body), &command)
	if err != nil || !input.Valid || len(input.Identity) <= 8192 {
		t.Fatalf("inspect: input=%d identity=%d %v", len(body), len(input.Identity), err)
	}
	if _, err := store.CreateExact(context.Background(), command, input.Identity); err != nil {
		t.Fatalf("create: %v", err)
	}
}

func TestCreateExactReplaysLegacyRejectedCommand(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	command := createCommand()
	command.Parameters.AreaM2 = 0
	if _, err := store.Create(ctx, command); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("legacy reject: %v", err)
	}
	body, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	var parsed contract.CreateTestCommand
	input, err := validator.Inspect("CreateTestCommand", body, &parsed)
	if err != nil || input.Valid {
		t.Fatalf("inspect: %+v %v", input, err)
	}
	status, err := store.CreateExact(ctx, parsed, input.Identity)
	if !errors.Is(err, ErrInvalidCommand) || status.Status != "rejected" {
		t.Fatalf("replay: %+v %v", status, err)
	}
	command.Parameters.PmaxPa++
	body, err = json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	input, err = validator.Inspect("CreateTestCommand", body, &parsed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateExact(ctx, parsed, input.Identity); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("changed rejection: %v", err)
	}
}

func TestLegacyRejectedLongInputSurvivesCanonicalExpansion(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	command := createCommand()
	command.Parameters.Valve = strings.Repeat("V", 7900)
	command.Parameters.AreaM2 = math.SmallestNonzeroFloat64
	command.Parameters.PmaxPa = math.SmallestNonzeroFloat64
	command.Parameters.MeasurementSeconds = math.SmallestNonzeroFloat64
	if _, err := store.Create(ctx, command); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("legacy reject: %v", err)
	}
	body, err := json.Marshal(command)
	if err != nil || len(body) > 8192 {
		t.Fatalf("legacy request size: %d %v", len(body), err)
	}
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	var parsed contract.CreateTestCommand
	input, err := validator.Inspect("CreateTestCommand", body, &parsed)
	if err != nil || input.Valid || len(input.Identity) <= 8704 {
		t.Fatalf("identity: %d %v", len(input.Identity), err)
	}
	status, err := store.LookupCreateExact(ctx, parsed, input.Identity)
	if !errors.Is(err, ErrInvalidCommand) || status.Status != "rejected" {
		t.Fatalf("legacy lookup: %+v %v", status, err)
	}
}

func TestLargeLegacyRejectedRequestIsConflictNotStorageFailure(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	old := createCommand()
	old.Parameters.Valve = strings.Repeat("V", 60000)
	if _, err := store.Create(ctx, old); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("legacy reject: %v", err)
	}
	current := old
	current.Parameters.Valve = "V"
	body, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	var parsed contract.CreateTestCommand
	input, err := validator.Inspect("CreateTestCommand", body, &parsed)
	if err != nil || !input.Valid {
		t.Fatalf("input: %v", err)
	}
	if _, err := store.LookupCreateExact(ctx, parsed, input.Identity); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("legacy conflict: %v", err)
	}
	newCommand := createCommand()
	if _, err := store.Create(ctx, newCommand); err != nil {
		t.Fatalf("storage should stay writable: %v", err)
	}
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

func TestMigrationKeepsVersionOneHistory(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "demo.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE meta (id INTEGER PRIMARY KEY CHECK (id = 1), revision INTEGER NOT NULL CHECK (revision >= 0))`,
		`INSERT INTO meta VALUES (1, 7)`,
		`CREATE TABLE tests (id TEXT PRIMARY KEY, record BLOB NOT NULL, stage TEXT NOT NULL)`,
		`CREATE TABLE commands (id TEXT PRIMARY KEY, operation TEXT NOT NULL, request BLOB NOT NULL, status TEXT NOT NULL, test_id TEXT REFERENCES tests(id), reason TEXT, operator_id TEXT NOT NULL, occurred_at TEXT NOT NULL)`,
		`CREATE TABLE audit (sequence INTEGER PRIMARY KEY AUTOINCREMENT, test_id TEXT NOT NULL REFERENCES tests(id), occurred_at TEXT NOT NULL, operator_id TEXT NOT NULL, action TEXT NOT NULL, outcome TEXT NOT NULL, revision INTEGER NOT NULL, command_id TEXT, action_id TEXT, reason TEXT)`,
		`CREATE INDEX audit_by_test ON audit(test_id, sequence)`,
		`PRAGMA user_version = 1`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	command := createCommand()
	command.Parameters.MeasurementSeconds = 7200
	testID, stopID := uuid.New(), uuid.New()
	old := contract.TestRecord{TestId: testID, StopCommandId: stopID, Demo: true, OperatorId: "demo-operator", Parameters: command.Parameters, Stage: "preparing", Outcome: "pending", DataQuality: "pending", SaveStatus: "saved", CreatedAt: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
	oldJSON, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tests(id, record, stage) VALUES (?, ?, 'preparing')`, testID.String(), oldJSON); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO commands(id, operation, request, status, test_id, operator_id, occurred_at) VALUES (?, 'create', ?, 'completed', ?, 'demo-operator', '2026-10-29T12:00:00Z')`, command.CommandId.String(), request, testID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO commands(id, operation, request, status, test_id, operator_id, occurred_at) VALUES (?, 'stop', x'', 'reserved', ?, 'demo-operator', '2026-10-29T12:00:00Z')`, stopID.String(), testID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit(test_id, occurred_at, operator_id, action, outcome, revision) VALUES (?, '2026-10-29T12:00:00Z', 'demo-operator', 'create', 'completed', 7)`, testID.String()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	var version, revision, events int
	var operation, status string
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 2 {
		t.Fatalf("version %d: %v", version, err)
	}
	if err := store.db.QueryRow(`SELECT revision FROM meta WHERE id = 1`).Scan(&revision); err != nil || revision != 7 {
		t.Fatalf("revision %d: %v", revision, err)
	}
	reopened, err := store.Test(context.Background(), testID)
	if err != nil || reopened != old {
		t.Fatalf("old record: %+v %v", reopened, err)
	}
	items, next, err := store.ListTests(context.Background(), nil, 1)
	if err != nil || len(items) != 1 || items[0] != old || next != nil {
		t.Fatalf("old history page: %+v %v", items, err)
	}
	storedCommand, err := store.Command(context.Background(), command.CommandId)
	if err != nil || storedCommand.Status != "completed" || storedCommand.TestId == nil || *storedCommand.TestId != testID {
		t.Fatalf("old stored command: %+v %v", storedCommand, err)
	}
	oldAudit, err := store.AuditPage(context.Background(), testID, 0, 1)
	if err != nil || len(oldAudit.Items) != 1 || oldAudit.NextAfter != nil {
		t.Fatalf("old audit: %+v %v", oldAudit, err)
	}
	repeated, err := store.Create(context.Background(), command)
	if err != nil || repeated.TestId == nil || *repeated.TestId != testID {
		t.Fatalf("old command: %+v %v", repeated, err)
	}
	if err := store.db.QueryRow(`SELECT operation, status FROM commands WHERE id = ?`, stopID.String()).Scan(&operation, &status); err != nil || operation != "stop" || status != "reserved" {
		t.Fatalf("reservation %s %s: %v", operation, status, err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM audit WHERE test_id = ? AND sequence = 1`, testID.String()).Scan(&events); err != nil || events != 1 {
		t.Fatalf("audit %d: %v", events, err)
	}
	if err := store.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.Test(context.Background(), testID)
	if err != nil || recovered.Stage != "finished" || recovered.Outcome != "unknown" || recovered.Parameters.MeasurementSeconds != 7200 {
		t.Fatalf("recovered old record: %+v %v", recovered, err)
	}
}

func TestFailedMigrationLeavesVersionOneUntouched(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "demo.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE measurements (unrelated TEXT); PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(dir); err == nil {
		_ = store.Close()
		t.Fatal("conflicting migration succeeded")
	}
	db, err = sql.Open("sqlite", filepath.Join(dir, "demo.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var version, pending int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("version %d: %v", version, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'test_actions'`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("partial migration %d: %v", pending, err)
	}
}

func TestSQLiteSettingsAreEnforcedAndReported(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	info := store.Settings()
	var mode, version string
	var synchronous, foreignKeys int
	if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT sqlite_version()`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" || synchronous != 2 || foreignKeys != 1 || info.Version != version || info.JournalMode != mode || info.Synchronous != "FULL" || version == "" {
		t.Fatalf("settings: %+v, %s %d %d %s", info, mode, synchronous, foreignKeys, version)
	}
}

func TestProfilePathWithHashUsesTheNamedDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo # профиль")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "demo.sqlite")); err != nil {
		t.Fatal(err)
	}
}

func TestNewDemoDurationIsBoundedAfterReplayLookup(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	command := createCommand()
	command.Parameters.MeasurementSeconds = 3601
	if _, err := store.Create(context.Background(), command); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("new oversized duration: %v", err)
	}
}
