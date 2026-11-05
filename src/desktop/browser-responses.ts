import type * as Contract from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';

export type ResponseGuard<T> = (value: unknown) => value is T;

function fields(
  value: unknown,
  required: string[],
  optional: string[] = [],
): value is Record<string, unknown> {
  return (
    typeof value === 'object' &&
    value !== null &&
    !Array.isArray(value) &&
    required.every((key) => Object.hasOwn(value, key)) &&
    Object.keys(value).every((key) => required.includes(key) || optional.includes(key))
  );
}

const text = (value: unknown): value is string => typeof value === 'string';
const finite = (value: unknown): value is number =>
  typeof value === 'number' && Number.isFinite(value);
const counter = (value: unknown, minimum = 0): value is number =>
  typeof value === 'number' && Number.isSafeInteger(value) && value >= minimum;
const uuid = (value: unknown): value is string =>
  text(value) && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value);
const nonzeroUUID = (value: unknown): value is string =>
  uuid(value) && value !== '00000000-0000-0000-0000-000000000000';
const oneOf = (value: unknown, values: string[]): boolean => text(value) && values.includes(value);
const optionalNullable = (
  value: Record<string, unknown>,
  key: string,
  guard: (item: unknown) => boolean,
): boolean => !Object.hasOwn(value, key) || value[key] === null || guard(value[key]);

// RFC 3339 calendar/time checks, including the leap-second rule used by the Go format validator.
function dateTime(value: unknown): value is string {
  if (!text(value)) return false;
  const match =
    /^(\d{4})-(\d{2})-(\d{2})[Tt](\d{2}):(\d{2}):(\d{2})(?:\.\d+)?([Zz]|[+-]\d{2}:\d{2})$/.exec(
      value,
    );
  if (!match) return false;
  const year = Number(match[1]),
    month = Number(match[2]),
    day = Number(match[3]);
  const hour = Number(match[4]),
    minute = Number(match[5]),
    second = Number(match[6]);
  const zone = match[7];
  const days = [
    31,
    year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0) ? 29 : 28,
    31,
    30,
    31,
    30,
    31,
    31,
    30,
    31,
    30,
    31,
  ];
  if (
    month < 1 ||
    month > 12 ||
    day < 1 ||
    day > (days[month - 1] ?? 0) ||
    hour > 23 ||
    minute > 59 ||
    second > 60 ||
    zone === undefined
  )
    return false;
  let utcMinute = hour * 60 + minute;
  if (zone !== 'Z' && zone !== 'z') {
    const hours = Number(zone.slice(1, 3)),
      minutes = Number(zone.slice(4));
    if (hours > 23 || minutes > 59) return false;
    utcMinute -= (zone[0] === '+' ? 1 : -1) * (hours * 60 + minutes);
  }
  return second < 60 || (utcMinute + 1440) % 1440 === 1439;
}

function handshake(value: unknown): value is Contract.Handshake {
  return (
    fields(value, ['apiVersion', 'backendRunId', 'databaseId', 'demo']) &&
    text(value['apiVersion']) &&
    nonzeroUUID(value['backendRunId']) &&
    nonzeroUUID(value['databaseId']) &&
    value['demo'] === true
  );
}

function connection(value: unknown): value is Contract.ConnectionState {
  return (
    fields(value, [
      'hostRunId',
      'revision',
      'status',
      'linkValid',
      'leaseAcquireUnknown',
      'streamGeneration',
    ]) &&
    uuid(value['hostRunId']) &&
    counter(value['revision']) &&
    counter(value['streamGeneration']) &&
    oneOf(value['status'], ['disconnected', 'recovering', 'live']) &&
    typeof value['linkValid'] === 'boolean' &&
    typeof value['leaseAcquireUnknown'] === 'boolean'
  );
}

function desktopStatus(value: unknown): value is Contract.DesktopStatus {
  return (
    fields(value, [
      'hostRunId',
      'revision',
      'connectionRevision',
      'status',
      'handshake',
      'diagnosticsDegraded',
    ]) &&
    uuid(value['hostRunId']) &&
    counter(value['revision']) &&
    counter(value['connectionRevision']) &&
    typeof value['diagnosticsDegraded'] === 'boolean' &&
    oneOf(value['status'], [
      'starting',
      'ready',
      'profileInUse',
      'unavailable',
      'exited',
      'closing',
    ]) &&
    (value['status'] === 'ready'
      ? handshake(value['handshake']) && value['handshake'].apiVersion === 'v1'
      : value['handshake'] === null)
  );
}

function parameters(value: unknown): value is Contract.TestParameters {
  return (
    fields(value, ['valve', 'areaM2', 'pmaxPa', 'measurementSeconds', 'direction', 'actuator']) &&
    text(value['valve']) &&
    [...value['valve']].length >= 1 &&
    [...value['valve']].length <= 200 &&
    finite(value['areaM2']) &&
    value['areaM2'] > 0 &&
    finite(value['pmaxPa']) &&
    value['pmaxPa'] > 0 &&
    finite(value['measurementSeconds']) &&
    value['measurementSeconds'] > 0 &&
    value['direction'] === 'pressure' &&
    value['actuator'] === false
  );
}

function testRecord(value: unknown): value is Contract.TestRecord {
  return (
    fields(
      value,
      [
        'testId',
        'demo',
        'operatorId',
        'parameters',
        'stopCommandId',
        'stage',
        'outcome',
        'dataQuality',
        'saveStatus',
        'createdAt',
      ],
      ['finishedAt'],
    ) &&
    uuid(value['testId']) &&
    uuid(value['stopCommandId']) &&
    value['demo'] === true &&
    text(value['operatorId']) &&
    parameters(value['parameters']) &&
    oneOf(value['stage'], ['preparing', 'running', 'stopping', 'finished']) &&
    oneOf(value['outcome'], ['pending', 'completed', 'interrupted', 'unknown']) &&
    oneOf(value['dataQuality'], ['pending', 'complete', 'incomplete', 'unknown']) &&
    oneOf(value['saveStatus'], ['saved', 'pending', 'failed', 'unknown']) &&
    dateTime(value['createdAt']) &&
    optionalNullable(value, 'finishedAt', dateTime)
  );
}

function measurement(value: unknown): value is Contract.Measurement {
  return (
    fields(value, [
      'sequence',
      'channel',
      'quantity',
      'value',
      'unit',
      'observedAt',
      'measuredAt',
      'quality',
      'source',
    ]) &&
    counter(value['sequence'], 1) &&
    oneOf(value['channel'], ['dd1', 'dd2', 'dd3']) &&
    value['quantity'] === (value['channel'] === 'dd1' ? 'pressure' : 'differentialPressure') &&
    value['unit'] === 'Pa' &&
    value['source'] === 'simulator' &&
    dateTime(value['observedAt']) &&
    oneOf(value['quality'], ['valid', 'stale', 'unavailable']) &&
    (value['quality'] === 'unavailable'
      ? value['value'] === null && value['measuredAt'] === null
      : finite(value['value']) && dateTime(value['measuredAt']))
  );
}

function pendingAction(value: unknown): value is Contract.PendingAction {
  return (
    fields(value, ['actionId', 'testId', 'instruction']) &&
    uuid(value['actionId']) &&
    uuid(value['testId']) &&
    text(value['instruction'])
  );
}

function commandStatus(value: unknown): value is Contract.CommandStatus {
  return (
    fields(value, ['commandId', 'status'], ['testId', 'reason']) &&
    uuid(value['commandId']) &&
    oneOf(value['status'], ['accepted', 'completed', 'rejected', 'failed', 'unknown']) &&
    optionalNullable(value, 'testId', uuid) &&
    optionalNullable(value, 'reason', text)
  );
}

function commandDispatch(value: unknown): value is Contract.CommandDispatch {
  return (
    fields(value, ['delivery', 'command']) &&
    oneOf(value['delivery'], ['notSent', 'conflict', 'response']) &&
    (value['delivery'] === 'response' ? commandStatus(value['command']) : value['command'] === null)
  );
}

function exactLookup(value: unknown): value is Contract.ExactLookupResult {
  return (
    fields(value, ['outcome', 'command']) &&
    oneOf(value['outcome'], ['match', 'unknown', 'conflict', 'databaseMismatch']) &&
    (value['outcome'] === 'match'
      ? commandStatus(value['command']) && value['command'].status !== 'unknown'
      : value['command'] === null)
  );
}

function measurementTiming(value: unknown): value is Contract.MeasurementTiming {
  return (
    fields(value, ['elapsedSeconds', 'remainingSeconds']) &&
    finite(value['elapsedSeconds']) &&
    value['elapsedSeconds'] >= 0 &&
    value['elapsedSeconds'] <= Number.MAX_SAFE_INTEGER &&
    finite(value['remainingSeconds']) &&
    value['remainingSeconds'] >= 0 &&
    value['remainingSeconds'] <= 3600
  );
}

function snapshot(value: unknown): value is Contract.Snapshot {
  return (
    fields(value, [
      'apiVersion',
      'backendRunId',
      'revision',
      'lastEventSequence',
      'activeTest',
      'pendingAction',
      'measurements',
      'measurementBatch',
      'measurementTiming',
    ]) &&
    text(value['apiVersion']) &&
    uuid(value['backendRunId']) &&
    counter(value['revision']) &&
    counter(value['lastEventSequence']) &&
    (value['activeTest'] === null || testRecord(value['activeTest'])) &&
    (value['pendingAction'] === null || pendingAction(value['pendingAction'])) &&
    (value['measurementTiming'] === null ||
      (measurementTiming(value['measurementTiming']) &&
        value['measurementBatch'] !== null &&
        testRecord(value['activeTest']) &&
        value['activeTest'].stage === 'running' &&
        value['pendingAction'] === null)) &&
    Array.isArray(value['measurements']) &&
    value['measurements'].length <= 3 &&
    value['measurements'].every(measurement) &&
    (value['measurementBatch'] === null
      ? value['measurements'].length === 0
      : counter(value['measurementBatch'], 1) &&
        value['measurements'].length > 0 &&
        value['activeTest'] !== null)
  );
}

function sessionView(value: unknown): value is Contract.SessionView {
  return (
    fields(value, ['connection', 'snapshot', 'validForMs', 'linkValidForMs']) &&
    connection(value['connection']) &&
    (value['snapshot'] === null || snapshot(value['snapshot'])) &&
    counter(value['validForMs']) &&
    counter(value['linkValidForMs'])
  );
}

function event(value: unknown): value is Contract.Event {
  if (
    !fields(
      value,
      ['type', 'backendRunId', 'sequence', 'revision'],
      ['testId', 'measurement', 'command'],
    ) ||
    !uuid(value['backendRunId']) ||
    !counter(value['sequence'], 1) ||
    !counter(value['revision']) ||
    !optionalNullable(value, 'testId', uuid)
  )
    return false;
  if (value['type'] === 'measurement')
    return (
      uuid(value['testId']) && measurement(value['measurement']) && !Object.hasOwn(value, 'command')
    );
  if (value['type'] === 'commandChanged')
    return commandStatus(value['command']) && !Object.hasOwn(value, 'measurement');
  return (
    value['type'] === 'stateChanged' &&
    !Object.hasOwn(value, 'measurement') &&
    !Object.hasOwn(value, 'command')
  );
}

function notice(value: unknown): value is Contract.ConnectionNotice {
  return (
    fields(value, ['connection', 'event', 'gap', 'snapshotBoundary']) &&
    connection(value['connection']) &&
    (value['event'] === null || event(value['event'])) &&
    typeof value['gap'] === 'boolean' &&
    counter(value['snapshotBoundary'])
  );
}

function readinessCheck(value: unknown): value is Contract.ReadinessCheck {
  return (
    fields(value, ['code', 'label', 'ready', 'source']) &&
    text(value['code']) &&
    text(value['label']) &&
    typeof value['ready'] === 'boolean' &&
    oneOf(value['source'], ['simulator', 'core'])
  );
}

function readiness(value: unknown): value is Contract.Readiness {
  return (
    fields(value, ['testId', 'revision', 'ready', 'checks']) &&
    uuid(value['testId']) &&
    counter(value['revision']) &&
    typeof value['ready'] === 'boolean' &&
    Array.isArray(value['checks']) &&
    value['checks'].every(readinessCheck)
  );
}

function auditEvent(value: unknown): value is Contract.AuditEvent {
  return (
    fields(
      value,
      ['sequence', 'testId', 'occurredAt', 'operatorId', 'action', 'outcome', 'revision'],
      ['commandId', 'actionId', 'instruction', 'reason'],
    ) &&
    counter(value['sequence'], 1) &&
    uuid(value['testId']) &&
    dateTime(value['occurredAt']) &&
    text(value['operatorId']) &&
    text(value['action']) &&
    text(value['outcome']) &&
    counter(value['revision']) &&
    optionalNullable(value, 'commandId', uuid) &&
    optionalNullable(value, 'actionId', uuid) &&
    optionalNullable(value, 'instruction', text) &&
    optionalNullable(value, 'reason', text)
  );
}

function measurementPage(value: unknown): value is Contract.MeasurementPage {
  return (
    fields(value, ['items', 'nextAfter']) &&
    Array.isArray(value['items']) &&
    value['items'].length <= 500 &&
    value['items'].every(measurement) &&
    (value['nextAfter'] === null || (counter(value['nextAfter'], 1) && value['items'].length > 0))
  );
}

function auditPage(value: unknown): value is Contract.AuditPage {
  return (
    fields(value, ['items', 'nextAfter']) &&
    Array.isArray(value['items']) &&
    value['items'].length <= 500 &&
    value['items'].every(auditEvent) &&
    (value['nextAfter'] === null || (counter(value['nextAfter'], 1) && value['items'].length > 0))
  );
}

function testList(value: unknown): value is Contract.TestList {
  return (
    fields(value, ['items', 'nextAfter']) &&
    Array.isArray(value['items']) &&
    value['items'].length <= 500 &&
    value['items'].every(testRecord) &&
    (value['nextAfter'] === null || uuid(value['nextAfter']))
  );
}

function ticket(value: unknown): value is { ticket: string } {
  return fields(value, ['ticket']) && nonzeroUUID(value['ticket']);
}

function browserControl(value: unknown): value is Record<string, unknown> {
  if (fields(value, ['retry'])) return value['retry'] === true;
  if (
    !fields(value, ['channel'], ['busy']) ||
    !text(value['channel']) ||
    !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}:[1-9][0-9]*$/.test(
      value['channel'],
    )
  )
    return false;
  return !Object.hasOwn(value, 'busy') || value['busy'] === true;
}

export const responseGuards = {
  Handshake: handshake,
  ConnectionState: connection,
  DesktopStatus: desktopStatus,
  TestParameters: parameters,
  TestRecord: testRecord,
  Measurement: measurement,
  PendingAction: pendingAction,
  CommandStatus: commandStatus,
  CommandDispatch: commandDispatch,
  ExactLookupResult: exactLookup,
  Snapshot: snapshot,
  SessionView: sessionView,
  Event: event,
  ConnectionNotice: notice,
  ReadinessCheck: readinessCheck,
  Readiness: readiness,
  AuditEvent: auditEvent,
  MeasurementPage: measurementPage,
  AuditPage: auditPage,
  TestList: testList,
  BrowserSessionTicket: ticket,
  BrowserControl: browserControl,
};

// A decimal may round to an integer (or zero) before a shape guard can inspect it.
function integralDecimal(token: string): boolean {
  const [coefficient = '', exponent = '0'] = token.toLowerCase().split('e');
  const [whole = '', fraction = ''] = coefficient.replace('-', '').split('.');
  const digits = whole + fraction;
  if (/^0+$/.test(digits)) return true;
  const places = fraction.length - Number(exponent);
  return places <= 0 || (places <= digits.length && /^0+$/.test(digits.slice(-places)));
}

function wellFormed(value: string): boolean {
  for (let index = 0; index < value.length; index++) {
    const code = value.charCodeAt(index);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(++index);
      if (!(next >= 0xdc00 && next <= 0xdfff)) return false;
    } else if (code >= 0xdc00 && code <= 0xdfff) return false;
  }
  return true;
}

// JSON.parse owns syntax. This scan retains only evidence it would erase.
export function parseBrowserJSON(body: string): unknown {
  const result: unknown = JSON.parse(body);
  const containers: (Set<string> | null)[] = [];
  const tokens =
    /("(?:\\.|[^"\\])*")\s*:|"(?:\\.|[^"\\])*"|(-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?)|[{}[\]]/g;
  for (const match of body.matchAll(tokens)) {
    const token = match[0];
    if (token === '{') containers.push(new Set());
    else if (token === '[') containers.push(null);
    else if (token === '}' || token === ']') containers.pop();
    else if (match[2] !== undefined) {
      const number = Number(match[2]);
      if (!Number.isFinite(number) || (Number.isInteger(number) && !integralDecimal(match[2])))
        throw new Error('unrepresentable browser number');
    } else {
      const value: unknown = JSON.parse(match[1] ?? token);
      if (!text(value) || !wellFormed(value)) throw new Error('invalid browser string');
      if (match[1] !== undefined) {
        const current = containers[containers.length - 1];
        if (!current || current.has(value)) throw new Error('ambiguous browser JSON');
        current.add(value);
      }
    }
  }
  return result;
}
