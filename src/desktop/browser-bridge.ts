import type { DesktopBridge, SessionBridge } from './bridge';
import {
  parseBrowserJSON,
  type ResponseGuard,
  responseGuards as responses,
} from './browser-responses';
import { type DemoService, normalizeCommandDispatch } from './demo-service';

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
  };
  function request<T>(
    path: string,
    validate: ResponseGuard<T>,
    options?: RequestOptions,
  ): Promise<T>;
  function request(
    path: string,
    validate: null,
    options: RequestOptions & { empty: true },
  ): Promise<void>;
  async function request(
    path: string,
    validate: ResponseGuard<unknown> | null,
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
        throw new Error('browser request unavailable');
      }
      if (response.status !== (options.empty ? 204 : 200))
        throw new Error('invalid browser response');
      const bytes = await response.arrayBuffer();
      ensureActive();
      // Preserve malformed bytes/BOM instead of silently replacing or discarding them.
      const body = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes);
      if (options.empty) {
        if (body !== '') throw new Error('invalid browser response');
        return;
      }
      const result = parseBrowserJSON(body);
      if (!validate?.(result)) throw new Error('invalid browser response');
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
  const post = <T>(path: string, validate: ResponseGuard<T>, body?: unknown) =>
    request(path, validate, {
      method: 'POST',
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });

  let statusGeneration = 0;
  const bridge: DesktopBridge = {
    status: () => {
      statusGeneration++;
      return request('/api/status', responses.DesktopStatus);
    },
    verify: () => {
      statusGeneration++;
      return request('/api/verify', responses.DesktopStatus, { method: 'POST' });
    },
    subscribe: (callback, onError) => {
      const controller = new AbortController();
      let timer: ReturnType<typeof setTimeout> | undefined;
      const poll = async () => {
        const generation = statusGeneration;
        try {
          const status = await request<Awaited<ReturnType<DesktopBridge['status']>>>(
            '/api/status',
            responses.DesktopStatus,
            { signal: controller.signal },
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
      const result = await post('/api/session/check', responses.BrowserSessionTicket);
      return result.ticket;
    },
    keepAlive: (ticket) =>
      request('/api/session/heartbeat', null, {
        method: 'POST',
        body: JSON.stringify({ ticket }),
        empty: true,
      }),
    sessionView: () => request('/api/session/view', responses.SessionView),
    observeSession: () => request('/api/session/observe', responses.SessionView),
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
        responses.ConnectionNotice,
      );
    },
  };
  const service: DemoService = {
    createTest: (command) =>
      post('/api/tests', responses.CommandDispatch, command).then(normalizeCommandDispatch),
    readiness: (testId) => request(`/api/tests/${segment(testId)}/readiness`, responses.Readiness),
    startTest: (testId, command) =>
      post(`/api/tests/${segment(testId)}/start`, responses.CommandDispatch, command).then(
        normalizeCommandDispatch,
      ),
    confirmAction: (testId, actionId, command) =>
      post(
        `/api/tests/${segment(testId)}/actions/${segment(actionId)}/confirm`,
        responses.CommandDispatch,
        command,
      ).then(normalizeCommandDispatch),
    stopTest: (testId, command) =>
      post(`/api/tests/${segment(testId)}/stop`, responses.CommandDispatch, command).then(
        normalizeCommandDispatch,
      ),
    command: (commandId) => request(`/api/commands/${segment(commandId)}`, responses.CommandStatus),
    lookupCommandExact: (databaseId, kind, testId, actionId, commandId, originalJSON) =>
      request(`/api/commands/${segment(commandId)}/lookup-exact`, responses.ExactLookupResult, {
        method: 'POST',
        body: originalJSON,
        headers: {
          'X-GK80-Database': databaseId,
          'X-GK80-Kind': kind,
          ...(testId ? { 'X-GK80-Test': testId } : {}),
          ...(actionId ? { 'X-GK80-Action': actionId } : {}),
        },
      }),
    listTests: (params) => {
      const query = new URLSearchParams();
      for (const [name, value] of Object.entries(params)) {
        if (value !== null && value !== undefined && (name !== 'q' || value !== ''))
          query.set(name, String(value));
      }
      return request(`/api/tests?${query}`, responses.TestList);
    },
    test: (testId) => request(`/api/tests/${segment(testId)}`, responses.TestRecord),
    measurements: (testId, after, limit) =>
      request(
        `/api/tests/${segment(testId)}/measurements${page(after, limit)}`,
        responses.MeasurementPage,
      ),
    audit: (testId, after, limit) =>
      request(`/api/tests/${segment(testId)}/audit${page(after, limit)}`, responses.AuditPage),
  };
  return { bridge, session, service };
}
