import {
  MeasurementChannel,
  MeasurementQuality,
  MeasurementQuantity,
  MeasurementSource,
  MeasurementUnit,
  TestParametersDirection,
  TestRecordDataQuality,
  TestRecordOutcome,
  TestRecordSaveStatus,
  TestRecordStage,
} from '../generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import type { DesktopBridge, SessionBridge } from './bridge';
import { CommandConflictError, type DemoService } from './demo-service';

function object(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function counter(value: unknown): boolean {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0;
}

function enumValue(value: unknown, values: object): boolean {
  return typeof value === 'string' && value !== '' && Object.values(values).includes(value);
}

function measurement(value: unknown): boolean {
  return (
    object(value) &&
    counter(value['sequence']) &&
    enumValue(value['channel'], MeasurementChannel) &&
    enumValue(value['quantity'], MeasurementQuantity) &&
    enumValue(value['quality'], MeasurementQuality) &&
    enumValue(value['source'], MeasurementSource) &&
    enumValue(value['unit'], MeasurementUnit) &&
    typeof value['observedAt'] === 'string' &&
    (value['measuredAt'] === null || typeof value['measuredAt'] === 'string') &&
    (value['value'] === null ||
      (typeof value['value'] === 'number' && Number.isFinite(value['value'])))
  );
}

function testRecord(value: unknown): boolean {
  if (!object(value) || !object(value['parameters'])) return false;
  const params = value['parameters'];
  return (
    typeof value['testId'] === 'string' &&
    typeof value['stopCommandId'] === 'string' &&
    typeof value['createdAt'] === 'string' &&
    typeof value['operatorId'] === 'string' &&
    typeof value['demo'] === 'boolean' &&
    enumValue(value['stage'], TestRecordStage) &&
    enumValue(value['outcome'], TestRecordOutcome) &&
    enumValue(value['saveStatus'], TestRecordSaveStatus) &&
    enumValue(value['dataQuality'], TestRecordDataQuality) &&
    typeof params['valve'] === 'string' &&
    typeof params['actuator'] === 'boolean' &&
    enumValue(params['direction'], TestParametersDirection) &&
    typeof params['areaM2'] === 'number' &&
    Number.isFinite(params['areaM2']) &&
    typeof params['pmaxPa'] === 'number' &&
    Number.isFinite(params['pmaxPa']) &&
    typeof params['measurementSeconds'] === 'number' &&
    Number.isFinite(params['measurementSeconds'])
  );
}

function connection(value: unknown): boolean {
  return (
    object(value) &&
    typeof value['hostRunId'] === 'string' &&
    counter(value['revision']) &&
    counter(value['streamGeneration']) &&
    typeof value['linkValid'] === 'boolean' &&
    typeof value['leaseAcquireUnknown'] === 'boolean' &&
    (value['status'] === 'live' ||
      value['status'] === 'recovering' ||
      value['status'] === 'disconnected')
  );
}

function sessionView(value: unknown): boolean {
  if (
    !object(value) ||
    !connection(value['connection']) ||
    !counter(value['validForMs']) ||
    !counter(value['linkValidForMs'])
  )
    return false;
  if (value['snapshot'] === null) return true;
  const snapshot = value['snapshot'];
  if (
    !object(snapshot) ||
    typeof snapshot['apiVersion'] !== 'string' ||
    typeof snapshot['backendRunId'] !== 'string' ||
    !counter(snapshot['revision']) ||
    !counter(snapshot['lastEventSequence']) ||
    !(
      snapshot['measurements'] === null ||
      (Array.isArray(snapshot['measurements']) && snapshot['measurements'].every(measurement))
    )
  )
    return false;
  if (snapshot['activeTest'] !== null && !testRecord(snapshot['activeTest'])) return false;
  return (
    snapshot['pendingAction'] === null ||
    (object(snapshot['pendingAction']) &&
      typeof snapshot['pendingAction']['testId'] === 'string' &&
      typeof snapshot['pendingAction']['actionId'] === 'string' &&
      typeof snapshot['pendingAction']['instruction'] === 'string')
  );
}

function notice(value: unknown): boolean {
  if (
    !object(value) ||
    !connection(value['connection']) ||
    typeof value['gap'] !== 'boolean' ||
    !counter(value['snapshotBoundary'])
  )
    return false;
  return (
    value['event'] === null ||
    (object(value['event']) &&
      typeof value['event']['backendRunId'] === 'string' &&
      counter(value['event']['sequence']))
  );
}

function desktopStatus(value: unknown): boolean {
  if (
    !object(value) ||
    typeof value['hostRunId'] !== 'string' ||
    !counter(value['revision']) ||
    !counter(value['connectionRevision']) ||
    typeof value['diagnosticsDegraded'] !== 'boolean' ||
    typeof value['status'] !== 'string' ||
    !['starting', 'ready', 'profileInUse', 'unavailable', 'closing', 'exited'].includes(
      value['status'],
    )
  )
    return false;
  if (value['handshake'] === null) return true;
  return (
    object(value['handshake']) &&
    typeof value['handshake']['apiVersion'] === 'string' &&
    typeof value['handshake']['backendRunId'] === 'string' &&
    typeof value['handshake']['databaseId'] === 'string' &&
    typeof value['handshake']['demo'] === 'boolean'
  );
}

// The generation associates requests with the current connection; it is not an access credential.
export function createBrowserBridge(
  channel: Readonly<{ generation: string; signal: AbortSignal }>,
  fetcher: typeof fetch = fetch,
): {
  bridge: DesktopBridge;
  session: SessionBridge;
  service: DemoService;
} {
  const { generation, signal: channelSignal } = channel;
  if (!generation) throw new Error('browser connection unavailable');
  type RequestOptions = {
    method?: 'GET' | 'POST';
    body?: string;
    headers?: Record<string, string>;
    signal?: AbortSignal;
    validate?: (value: unknown) => boolean;
  };
  function request<T>(path: string, options?: RequestOptions): Promise<T>;
  function request(path: string, options: RequestOptions & { empty: true }): Promise<void>;
  async function request(
    path: string,
    options: RequestOptions & { empty?: boolean } = {},
  ): Promise<unknown> {
    const controller = new AbortController();
    const abort = () => controller.abort();
    if (channelSignal.aborted || options.signal?.aborted)
      throw new Error('browser channel unavailable');
    channelSignal.addEventListener('abort', abort, { once: true });
    options.signal?.addEventListener('abort', abort, { once: true });
    const timeout = setTimeout(abort, 3500);
    const ensureActive = () => {
      if (controller.signal.aborted || channelSignal.aborted)
        throw new Error('browser channel unavailable');
    };
    try {
      ensureActive();
      const headers = new Headers(options.headers);
      headers.set('X-GK80-Channel', generation);
      if (options.body !== undefined) headers.set('Content-Type', 'application/json');
      const response = await fetcher(path, {
        method: options.method ?? 'GET',
        headers,
        credentials: 'same-origin',
        cache: 'no-store',
        redirect: 'error',
        ...(options.body === undefined ? {} : { body: options.body }),
        signal: controller.signal,
      });
      ensureActive();
      if (!response.ok) {
        if (response.status === 409) {
          const failure = await response.text().catch(() => '');
          ensureActive();
          if (failure.trim() === '{"error":"command_conflict"}') throw new CommandConflictError();
        }
        throw new Error('browser request unavailable');
      }
      if (options.empty) {
        if (response.status !== 204) throw new Error('invalid browser response');
        return;
      }
      if (response.status === 204) throw new Error('invalid browser response');
      const result: unknown = await response.json();
      ensureActive();
      if (typeof result !== 'object' || result === null || Array.isArray(result))
        throw new Error('invalid browser response');
      if (options.validate && !options.validate(result))
        throw new Error('invalid browser response');
      return result;
    } finally {
      clearTimeout(timeout);
      options.signal?.removeEventListener('abort', abort);
      channelSignal.removeEventListener('abort', abort);
    }
  }

  const segment = (value: string) => encodeURIComponent(value);
  const page = (after: string | number | null | undefined, limit: number | null | undefined) => {
    const query = new URLSearchParams();
    if (after !== null && after !== undefined) query.set('after', String(after));
    if (limit !== null && limit !== undefined) query.set('limit', String(limit));
    const value = query.toString();
    return value ? `?${value}` : '';
  };
  const post = <T>(path: string, body?: unknown) =>
    request<T>(path, {
      method: 'POST',
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });

  let statusGeneration = 0;
  const bridge: DesktopBridge = {
    status: () => {
      statusGeneration++;
      return request('/api/status', { validate: desktopStatus });
    },
    verify: () => {
      statusGeneration++;
      return request('/api/verify', { method: 'POST', validate: desktopStatus });
    },
    subscribe: (callback, onError) => {
      const controller = new AbortController();
      let timer: ReturnType<typeof setTimeout> | undefined;
      const poll = async () => {
        const generation = statusGeneration;
        try {
          const status = await request<Awaited<ReturnType<DesktopBridge['status']>>>(
            '/api/status',
            { signal: controller.signal, validate: desktopStatus },
          );
          if (
            !controller.signal.aborted &&
            !channelSignal.aborted &&
            generation === statusGeneration
          )
            callback(status, true);
        } catch {
          if (
            !controller.signal.aborted &&
            !channelSignal.aborted &&
            generation === statusGeneration
          )
            onError?.();
        }
        if (!controller.signal.aborted && !channelSignal.aborted) timer = setTimeout(poll, 1500);
      };
      const dispose = () => {
        controller.abort();
        if (timer) clearTimeout(timer);
        channelSignal.removeEventListener('abort', channelClosed);
      };
      const channelClosed = () => {
        if (controller.signal.aborted) return;
        dispose();
        onError?.();
      };
      if (!channelSignal.aborted) {
        channelSignal.addEventListener('abort', channelClosed, { once: true });
        timer = setTimeout(poll, 1500);
      } else queueMicrotask(channelClosed);
      return dispose;
    },
  };
  const session: SessionBridge = {
    beginSessionCheck: async () => {
      const result = await post<{ ticket: string }>('/api/session/check');
      if (typeof result.ticket !== 'string' || !result.ticket)
        throw new Error('invalid session ticket');
      return result.ticket;
    },
    keepAlive: (ticket) =>
      request('/api/session/heartbeat', {
        method: 'POST',
        body: JSON.stringify({ ticket }),
        empty: true,
      }),
    sessionView: () => request('/api/session/view', { validate: sessionView }),
    observeSession: () => request('/api/session/observe', { validate: sessionView }),
    waitConnection: (after, afterSequence) => {
      if (
        !Number.isSafeInteger(after) ||
        after < 0 ||
        !Number.isSafeInteger(afterSequence) ||
        afterSequence < 0
      )
        throw new Error('invalid event cursor');
      return request(
        `/api/session/wait?${new URLSearchParams({ after: String(after), afterSequence: String(afterSequence) })}`,
        { validate: notice },
      );
    },
  };
  const service: DemoService = {
    createTest: (command) => post('/api/tests', command),
    readiness: (testId) => request(`/api/tests/${segment(testId)}/readiness`),
    startTest: (testId, command) => post(`/api/tests/${segment(testId)}/start`, command),
    confirmAction: (testId, actionId, command) =>
      post(`/api/tests/${segment(testId)}/actions/${segment(actionId)}/confirm`, command),
    stopTest: (testId, command) => post(`/api/tests/${segment(testId)}/stop`, command),
    command: (commandId) => request(`/api/commands/${segment(commandId)}`),
    lookupCommandExact: (databaseId, kind, testId, actionId, commandId, originalJSON) =>
      request(`/api/commands/${segment(commandId)}/lookup-exact`, {
        method: 'POST',
        body: originalJSON,
        headers: {
          'X-GK80-Database': databaseId,
          'X-GK80-Kind': kind,
          ...(testId ? { 'X-GK80-Test': testId } : {}),
          ...(actionId ? { 'X-GK80-Action': actionId } : {}),
        },
      }),
    listTests: (params) => request(`/api/tests${page(params.after, params.limit)}`),
    test: (testId) => request(`/api/tests/${segment(testId)}`),
    measurements: (testId, after, limit) =>
      request(`/api/tests/${segment(testId)}/measurements${page(after, limit)}`),
    audit: (testId, after, limit) =>
      request(`/api/tests/${segment(testId)}/audit${page(after, limit)}`),
  };
  return { bridge, session, service };
}
