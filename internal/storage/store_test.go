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

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

func createCommand() contract.CreateTestCommand {
	return contract.CreateTestCommand{CommandId: uuid.New(), ExpectedRevision: 0, Parameters: contract.TestParameters{PreOpenStabilizationSeconds: 10, PostCloseStabilizationSeconds: 10,
		Valve: "Демо клапан", AreaM2: 0.01, PmaxPa: 1000, MeasurementSeconds: 60, Direction: "pressure", Actuator: false,
	}}
}

func TestManualParametersPersistAndValidateBoundaries(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	command := createCommand()
	command.Parameters.MeasurementSeconds = 300
	command.Parameters.PreOpenStabilizationSeconds = 100
	command.Parameters.PostCloseStabilizationSeconds = 10
	command.Parameters.PostCloseStabilizationRetries = 0
	result, err := store.Create(ctx, command)
	if err != nil || result.TestId == nil {
		t.Fatalf("create: %+v %v", result, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	record, err := store.Test(ctx, *result.TestId)
	if err != nil || record.Parameters != command.Parameters {
		t.Fatalf("reopen: %+v %v", record.Parameters, err)
	}
	status, err := store.LookupCreate(ctx, command)
	if err != nil || status.TestId == nil || *status.TestId != *result.TestId {
		t.Fatalf("lookup: %+v %v", status, err)
	}
	changed := command
	changed.Parameters.PostCloseStabilizationRetries = 1
	if _, err := store.LookupCreate(ctx, changed); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("changed retries did not conflict: %v", err)
	}
	for _, change := range []func(*contract.TestParameters){
		func(p *contract.TestParameters) { p.MeasurementSeconds = 59 },
		func(p *contract.TestParameters) { p.MeasurementSeconds = 300.1 },
		func(p *contract.TestParameters) { p.PreOpenStabilizationSeconds = 9 },
		func(p *contract.TestParameters) { p.PostCloseStabilizationSeconds = 100.5 },
		func(p *contract.TestParameters) { p.PostCloseStabilizationRetries = -1 },
		func(p *contract.TestParameters) { p.PostCloseStabilizationRetries = 101 },
	} {
		invalid := command
		invalid.CommandId = uuid.New()
		change(&invalid.Parameters)
		if valid(invalid) {
			t.Fatalf("accepted invalid parameters: %+v", invalid.Parameters)
		}
	}
}

func TestPreviousDemoProfileIsNotModified(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "demo.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE retained_data (value TEXT); INSERT INTO retained_data VALUES ('preserved'); PRAGMA user_version = 3`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(dir); err == nil {
		_ = store.Close()
		t.Fatal("old profile accepted")
	}
	db, err = sql.Open("sqlite", filepath.Join(dir, "demo.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var value string
	var version int
	if err := db.QueryRow(`SELECT value FROM retained_data`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if value != "preserved" || version != 3 {
		t.Fatalf("old profile changed: %q %d", value, version)
	}
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
	base := `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8","expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":60,"preOpenStabilizationSeconds":13,"postCloseStabilizationSeconds":17,"postCloseStabilizationRetries":0,"direction":"pressure","actuator":false}}`
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
	record, err := store.Test(ctx, *created.TestId)
	if err != nil || record.Parameters.PreOpenStabilizationSeconds != 13 || record.Parameters.PostCloseStabilizationSeconds != 17 {
		t.Fatalf("reopened stabilization parameters: %+v %v", record.Parameters, err)
	}
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
	body := `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8","expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":300.00000000000001,"preOpenStabilizationSeconds":10,"postCloseStabilizationSeconds":10,"postCloseStabilizationRetries":0,"direction":"pressure","actuator":false}}`
	var command contract.CreateTestCommand
	input, err := validator.Inspect("CreateTestCommand", []byte(body), &command)
	if err != nil || command.Parameters.MeasurementSeconds != 300 {
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
	body := `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8","expectedRevision":0,"parameters":{"valve":"` + strings.Repeat("<", 200) + `","areaM2":1.` + strings.Repeat("0", 7400) + `1,"pmaxPa":1000,"measurementSeconds":60,"preOpenStabilizationSeconds":10,"postCloseStabilizationSeconds":10,"postCloseStabilizationRetries":0,"direction":"pressure","actuator":false}}`
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
	command.Parameters.Valve = strings.Repeat("V", 7850)
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

func TestDatabaseIdentitySurvivesReopenAndRejectsCorruption(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var identity string
	if err := store.db.QueryRow(`SELECT database_id FROM meta WHERE id = 1`).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	parsed, err := uuid.Parse(identity)
	if err != nil || parsed == uuid.Nil || store.DatabaseID() != parsed {
		t.Fatalf("invalid database identity %q: %v", identity, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var reopened string
	if err := store.db.QueryRow(`SELECT database_id FROM meta WHERE id = 1`).Scan(&reopened); err != nil || reopened != identity {
		t.Fatalf("identity after reopen %q: %v", reopened, err)
	}
	if store.DatabaseID() != parsed {
		t.Fatalf("reported identity after reopen: %s", store.DatabaseID())
	}
	other, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var otherID string
	if err := other.db.QueryRow(`SELECT database_id FROM meta WHERE id = 1`).Scan(&otherID); err != nil || otherID == identity {
		t.Fatalf("other database identity %q: %v", otherID, err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE meta SET database_id = ? WHERE id = 1`, uuid.Nil.String()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if corrupt, err := Open(dir); err == nil {
		_ = corrupt.Close()
		t.Fatal("database with a nil identity was accepted")
	}
}

func TestOpenRejectsInvalidDatabaseIdentity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
	}{
		{name: "missing", query: `DELETE FROM meta WHERE id = 1`},
		{name: "null", query: `UPDATE meta SET database_id = NULL WHERE id = 1`},
		{name: "malformed", query: `UPDATE meta SET database_id = 'invalid' WHERE id = 1`},
		{name: "not random UUID", query: `UPDATE meta SET database_id = '00000000-0000-0000-0000-000000000001' WHERE id = 1`},
		{name: "wrong version", query: `UPDATE meta SET database_id = '00000000-0000-1000-8000-000000000001' WHERE id = 1`},
		{name: "wrong variant", query: `UPDATE meta SET database_id = '00000000-0000-4000-0000-000000000001' WHERE id = 1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			store, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(tc.query); err != nil {
				_ = store.Close()
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if corrupt, err := Open(dir); err == nil {
				_ = corrupt.Close()
				t.Fatal("invalid database identity accepted")
			}
		})
	}
}

func TestPreviousVersionOneProfileRemainsUntouched(t *testing.T) {
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
