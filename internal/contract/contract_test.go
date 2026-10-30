package contract

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

func schemaFor(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	data, err := os.ReadFile("../../api/core.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	document["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("https://gk80.invalid/core.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("https://gk80.invalid/core.json#/components/schemas/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func validateJSON(t *testing.T, schema *jsonschema.Schema, value any, valid bool) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	err = schema.Validate(decoded)
	if (err == nil) != valid {
		t.Fatalf("valid=%t: %s: %v", valid, encoded, err)
	}
}

func TestMeasurementContractPreservesNullAndRejectsInvalidData(t *testing.T) {
	schema := schemaFor(t, "Measurement")
	measurement := Measurement{
		Sequence: 1, Channel: Dd1, Quantity: MeasurementQuantityPressure, Unit: Pa,
		ObservedAt: time.Date(2026, 10, 29, 12, 0, 1, 0, time.UTC),
		Quality:    MeasurementQualityUnavailable, Source: MeasurementSourceSimulator,
	}
	validateJSON(t, schema, measurement, true)
	zero := 0.0
	measuredAt := "2026-10-29T12:00:00Z"
	measurement.Value = &zero
	measurement.MeasuredAt = &measuredAt
	measurement.Quality = MeasurementQualityValid
	validateJSON(t, schema, measurement, true)
	measurement.Sequence = 0
	validateJSON(t, schema, measurement, false)
	measurement.Sequence = 1
	measurement.Quantity = MeasurementQuantityDifferentialPressure
	validateJSON(t, schema, measurement, false)
	validateJSON(t, schema, map[string]any{
		"sequence": 1, "channel": "dd1", "quantity": "pressure", "unit": "Pa",
		"observedAt": "2026-10-29T12:00:01Z", "measuredAt": "not-a-date", "quality": "valid", "source": "simulator", "value": 0,
	}, false)
	validateJSON(t, schema, map[string]any{
		"sequence": 1, "channel": "dd1", "quantity": "pressure", "unit": "Pa",
		"observedAt": "2026-10-29T12:00:01Z", "measuredAt": "2026-10-29T12:00:00Z", "quality": "valid", "source": "simulator",
	}, false)
}

func TestEventRequiresPayloadForItsType(t *testing.T) {
	schema := schemaFor(t, "Event")
	runID := uuid.New()
	testID := uuid.New()
	measurement := Measurement{
		Sequence: 1, Channel: Dd1, Quantity: MeasurementQuantityPressure, Unit: Pa,
		ObservedAt: time.Date(2026, 10, 29, 12, 0, 1, 0, time.UTC),
		Quality:    MeasurementQualityUnavailable, Source: MeasurementSourceSimulator,
	}
	event := Event{BackendRunId: runID, Sequence: 1, Revision: 1, Type: EventTypeMeasurement, TestId: &testID, Measurement: &measurement}
	validateJSON(t, schema, event, true)
	event.Measurement = nil
	validateJSON(t, schema, event, false)
	event.Type = EventTypeStateChanged
	validateJSON(t, schema, event, true)
	event.Measurement = &measurement
	validateJSON(t, schema, event, false)
}

func TestStartRequiresBothConfirmations(t *testing.T) {
	schema := schemaFor(t, "StartTestCommand")
	command := StartTestCommand{CommandId: uuid.New(), ExpectedRevision: 1, ValveClosed: true, ConnectionConfirmed: true}
	validateJSON(t, schema, command, true)
	command.ConnectionConfirmed = false
	validateJSON(t, schema, command, false)
}

func TestAcceptedResponseCannotClaimRejected(t *testing.T) {
	schema := schemaFor(t, "CommandAccepted")
	response := CommandAccepted{CommandId: uuid.New(), Status: "accepted"}
	validateJSON(t, schema, response, true)
	response.Status = "rejected"
	validateJSON(t, schema, response, false)
}

func TestSnapshotUsesArrayForEmptyMeasurements(t *testing.T) {
	schema := schemaFor(t, "Snapshot")
	snapshot := Snapshot{ApiVersion: "v1", BackendRunId: uuid.New(), Measurements: []Measurement{}}
	validateJSON(t, schema, snapshot, true)
	snapshot.Measurements = nil
	validateJSON(t, schema, snapshot, false)
}

func TestPageCursorCannotPointToBeginning(t *testing.T) {
	for _, name := range []string{"MeasurementPage", "AuditPage"} {
		t.Run(name, func(t *testing.T) {
			schema := schemaFor(t, name)
			validateJSON(t, schema, map[string]any{"items": []any{}, "nextAfter": nil}, true)
			validateJSON(t, schema, map[string]any{"items": []any{}, "nextAfter": 0}, false)
			validateJSON(t, schema, map[string]any{"items": []any{}, "nextAfter": 1}, false)
		})
	}
}

func TestDesktopReadyRequiresConfirmedDemoHandshake(t *testing.T) {
	schema := schemaFor(t, "DesktopStatus")
	status := DesktopStatus{HostRunId: uuid.New(), Revision: 1, Status: DesktopStatusStatusStarting}
	validateJSON(t, schema, status, true)
	status.Status = DesktopStatusStatusReady
	validateJSON(t, schema, status, false)
	status.Handshake = &Handshake{ApiVersion: "v1", BackendRunId: uuid.New(), DatabaseId: uuid.New(), Demo: true}
	validateJSON(t, schema, status, true)
	status.Handshake.DatabaseId = uuid.Nil
	validateJSON(t, schema, status, false)
	status.Handshake.DatabaseId = uuid.New()
	status.Handshake.BackendRunId = uuid.Nil
	validateJSON(t, schema, status, false)
	status.Handshake.BackendRunId = uuid.New()
	status.Handshake.ApiVersion = "v2"
	validateJSON(t, schema, status, false)
	status.Status = DesktopStatusStatusExited
	validateJSON(t, schema, status, false)
}

func TestHistoricalMeasurementDurationRemainsValid(t *testing.T) {
	schema := schemaFor(t, "TestParameters")
	parameters := TestParameters{Valve: "Демо", AreaM2: 0.01, PmaxPa: 1000, MeasurementSeconds: 3600, Direction: "pressure"}
	validateJSON(t, schema, parameters, true)
	parameters.MeasurementSeconds = 3601
	validateJSON(t, schema, parameters, true)
	validateJSON(t, schemaFor(t, "CreateTestCommand"), CreateTestCommand{CommandId: uuid.New(), Parameters: parameters}, true)
}
