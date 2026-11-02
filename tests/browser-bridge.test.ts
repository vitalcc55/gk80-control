import { afterEach, expect, it, vi } from 'vitest';
import { createBrowserBridge } from '../src/desktop/browser-bridge';
import { CommandConflictError } from '../src/desktop/demo-service';

afterEach(() => vi.useRealTimers());

const channel = { capability: 'channel-proof', signal: new AbortController().signal };

it('rejects a closed channel before dispatching any HTTP operation', async () => {
  const owner = new AbortController();
  owner.abort();
  const request = vi.fn<typeof fetch>();
  const { service } = createBrowserBridge(
    'proof',
    { capability: 'closed', signal: owner.signal },
    request,
  );
  await expect(service.stopTest('test', { commandId: 'command' })).rejects.toThrow();
  expect(request).not.toHaveBeenCalled();
});

it('does not accept a late conflict or repeat a command after its channel closes', async () => {
  const owner = new AbortController();
  let finish: (value: Response) => void = () => {};
  const response = new Promise<Response>((resolve) => {
    finish = resolve;
  });
  const request = vi.fn<typeof fetch>().mockReturnValue(response);
  const { service } = createBrowserBridge(
    'proof',
    { capability: 'first', signal: owner.signal },
    request,
  );
  const outcome = service
    .stopTest('test', { commandId: 'command' })
    .catch((error: unknown) => error);
  owner.abort();
  finish(Response.json({ error: 'command_conflict' }, { status: 409 }));
  const error = await outcome;
  expect(error).toBeInstanceOf(Error);
  expect(error).not.toBeInstanceOf(CommandConflictError);
  expect(request).toHaveBeenCalledOnce();
});

it('aborts active reads and commands when their shared channel closes', async () => {
  const owner = new AbortController();
  let aborted = 0;
  const request = vi.fn<typeof fetch>(
    async (_path, options) =>
      await new Promise<Response>((_resolve, reject) => {
        options?.signal?.addEventListener(
          'abort',
          () => {
            aborted++;
            reject(new Error('aborted'));
          },
          { once: true },
        );
      }),
  );
  const { bridge, service } = createBrowserBridge(
    'proof',
    { capability: 'first', signal: owner.signal },
    request,
  );
  const read = bridge.status().catch((error: unknown) => error);
  const stop = service.stopTest('test', { commandId: 'command' }).catch((error: unknown) => error);
  expect(request).toHaveBeenCalledTimes(2);
  owner.abort();
  expect(aborted).toBe(2);
  expect(await read).toBeInstanceOf(Error);
  expect(await stop).toBeInstanceOf(Error);
  expect(request).toHaveBeenCalledTimes(2);
});

it('rejects a response whose body finishes after the channel closes', async () => {
  const owner = new AbortController();
  let finishBody: () => void = () => {};
  const body = new ReadableStream<Uint8Array>({
    start: (controller) => {
      finishBody = () => {
        controller.enqueue(new TextEncoder().encode('{"delivery":"notSent","command":null}'));
        controller.close();
      };
    },
  });
  const request = vi.fn<typeof fetch>().mockResolvedValue(new Response(body));
  const { service } = createBrowserBridge(
    'proof',
    { capability: 'first', signal: owner.signal },
    request,
  );
  const outcome = service
    .stopTest('test', { commandId: 'command' })
    .catch((error: unknown) => error);
  await Promise.resolve();
  owner.abort();
  finishBody();
  expect(await outcome).toBeInstanceOf(Error);
  expect(request).toHaveBeenCalledOnce();
});

it('uses a new capability after reconnect and rejects an old channel response', async () => {
  const first = new AbortController();
  let finish: (response: Response) => void = () => {};
  const pending = new Promise<Response>((resolve) => {
    finish = resolve;
  });
  const request = vi
    .fn<typeof fetch>()
    .mockReturnValueOnce(pending)
    .mockResolvedValueOnce(Response.json({ outcome: 'unknown', command: null }));
  const oldBridge = createBrowserBridge(
    'proof',
    { capability: 'old-channel', signal: first.signal },
    request,
  );
  const old = oldBridge.service
    .lookupCommandExact('database', 'stop', 'test', '', 'command', '{}')
    .catch((error: unknown) => error);
  first.abort();
  const second = new AbortController();
  const newBridge = createBrowserBridge(
    'proof',
    { capability: 'new-channel', signal: second.signal },
    request,
  );
  await newBridge.service.lookupCommandExact('database', 'stop', 'test', '', 'command', '{}');
  finish(
    Response.json({ outcome: 'match', command: { commandId: 'command', status: 'completed' } }),
  );
  expect(await old).toBeInstanceOf(Error);
  const oldHeaders = new Headers(request.mock.calls[0]?.[1]?.headers);
  const newHeaders = new Headers(request.mock.calls[1]?.[1]?.headers);
  expect(oldHeaders.get('X-GK80-Channel')).toBe('old-channel');
  expect(newHeaders.get('X-GK80-Channel')).toBe('new-channel');
});

it('sends the original Stop attempt and browser proof only in headers', async () => {
  const request = vi.fn<typeof fetch>(async () =>
    Response.json({ outcome: 'unknown', command: null }),
  );
  const { service } = createBrowserBridge('browser-proof', channel, request);
  const original = '{ "commandId" : "command" }';
  await service.lookupCommandExact('database', 'stop', 'test', '', 'command', original);
  expect(request).toHaveBeenCalledOnce();
  const [path, options] = request.mock.calls[0] ?? [];
  expect(path).toBe('/api/commands/command/lookup-exact');
  expect(options?.body).toBe(original);
  expect(options?.redirect).toBe('error');
  const headers = new Headers(options?.headers);
  expect(headers.get('X-GK80-Session')).toBe('browser-proof');
  expect(headers.get('X-GK80-Channel')).toBe('channel-proof');
  expect(headers.get('X-GK80-Database')).toBe('database');
  expect(headers.get('X-GK80-Kind')).toBe('stop');
  expect(headers.get('X-GK80-Test')).toBe('test');
  expect(headers.has('X-GK80-Action')).toBe(false);
  expect(String(path)).not.toContain('browser-proof');
});

it('distinguishes a confirmed command conflict and never retries a failed POST', async () => {
  const request = vi.fn<typeof fetch>();
  request.mockResolvedValueOnce(Response.json({ error: 'command_conflict' }, { status: 409 }));
  request.mockResolvedValueOnce(Response.json({ error: 'other' }, { status: 409 }));
  request.mockResolvedValueOnce(
    new Response('{"error":"other","error":"command_conflict"}', { status: 409 }),
  );
  request.mockRejectedValueOnce(new Error('connection lost'));
  const { service } = createBrowserBridge('proof', channel, request);
  const stop = { commandId: 'command' };
  await expect(service.stopTest('test', stop)).rejects.toBeInstanceOf(CommandConflictError);
  const different = await service.stopTest('test', stop).catch((error: unknown) => error);
  expect(different).toBeInstanceOf(Error);
  expect(different).not.toBeInstanceOf(CommandConflictError);
  const ambiguous = await service.stopTest('test', stop).catch((error: unknown) => error);
  expect(ambiguous).toBeInstanceOf(Error);
  expect(ambiguous).not.toBeInstanceOf(CommandConflictError);
  await expect(service.stopTest('test', stop)).rejects.toThrow();
  expect(request).toHaveBeenCalledTimes(4);
});

it('rejects malformed session data rather than confirming a connection', async () => {
  const request = vi.fn<typeof fetch>();
  request.mockResolvedValueOnce(Response.json({}));
  request.mockResolvedValueOnce(
    Response.json({
      connection: {
        hostRunId: 'host',
        revision: 1,
        streamGeneration: 1,
        status: 'live',
        linkValid: 'false',
        leaseAcquireUnknown: false,
      },
      snapshot: null,
      validForMs: 1000,
      linkValidForMs: 1000,
    }),
  );
  request.mockResolvedValueOnce(
    Response.json({
      connection: {
        hostRunId: 'host',
        revision: 1,
        streamGeneration: 1,
        status: 'live',
        linkValid: true,
        leaseAcquireUnknown: false,
      },
      snapshot: {
        apiVersion: 'v1',
        backendRunId: 'run',
        revision: 1,
        lastEventSequence: 1,
        activeTest: null,
        pendingAction: null,
        measurements: [null],
      },
      validForMs: 1000,
      linkValidForMs: 1000,
    }),
  );
  const { session } = createBrowserBridge('proof', channel, request);
  await expect(session.sessionView()).rejects.toThrow();
  await expect(session.sessionView()).rejects.toThrow();
  await expect(session.sessionView()).rejects.toThrow();
});

it('rejects a non-string desktop status', async () => {
  const request = vi.fn<typeof fetch>().mockResolvedValue(
    Response.json({
      hostRunId: 'host',
      revision: 1,
      connectionRevision: 1,
      diagnosticsDegraded: false,
      handshake: null,
      status: ['ready'],
    }),
  );
  await expect(createBrowserBridge('proof', channel, request).bridge.status()).rejects.toThrow();
});

it('accepts a complete session snapshot with a simulated measurement', async () => {
  const snapshot = {
    apiVersion: 'v1',
    backendRunId: 'run',
    revision: 1,
    lastEventSequence: 2,
    activeTest: {
      testId: 'test',
      stopCommandId: 'stop',
      createdAt: '2026-11-02T10:00:00Z',
      operatorId: 'demo',
      demo: true,
      stage: 'running',
      outcome: 'pending',
      saveStatus: 'pending',
      dataQuality: 'pending',
      parameters: {
        valve: 'V',
        areaM2: 0.01,
        pmaxPa: 1000,
        measurementSeconds: 1.5,
        direction: 'pressure',
        actuator: false,
      },
    },
    pendingAction: null,
    measurements: [
      {
        sequence: 1,
        channel: 'dd1',
        quantity: 'pressure',
        quality: 'valid',
        source: 'simulator',
        unit: 'Pa',
        observedAt: '2026-11-02T10:00:00Z',
        measuredAt: '2026-11-02T10:00:00Z',
        value: 12,
      },
    ],
  };
  const expected = {
    connection: {
      hostRunId: 'host',
      revision: 1,
      streamGeneration: 1,
      status: 'live',
      linkValid: true,
      leaseAcquireUnknown: false,
    },
    snapshot,
    validForMs: 1000,
    linkValidForMs: 1000,
  };
  const request = vi.fn<typeof fetch>().mockResolvedValue(Response.json(expected));
  await expect(
    createBrowserBridge('proof', channel, request).session.sessionView(),
  ).resolves.toEqual(expected);
});

it('reports failed status polling and stops on unsubscribe', async () => {
  vi.useFakeTimers();
  const request = vi.fn<typeof fetch>().mockRejectedValue(new Error('unavailable'));
  const { bridge } = createBrowserBridge('proof', channel, request);
  const onError = vi.fn();
  const unsubscribe = bridge.subscribe(vi.fn(), onError);
  await vi.advanceTimersByTimeAsync(1500);
  expect(onError).toHaveBeenCalledOnce();
  unsubscribe();
  await vi.advanceTimersByTimeAsync(5000);
  expect(request).toHaveBeenCalledOnce();
});

it('stops status polling when its browser channel closes', async () => {
  vi.useFakeTimers();
  const owner = new AbortController();
  const request = vi.fn<typeof fetch>();
  const { bridge } = createBrowserBridge(
    'proof',
    { capability: 'first', signal: owner.signal },
    request,
  );
  const onError = vi.fn();
  const unsubscribe = bridge.subscribe(vi.fn(), onError);
  owner.abort();
  await vi.advanceTimersByTimeAsync(5000);
  expect(request).not.toHaveBeenCalled();
  expect(onError).toHaveBeenCalledOnce();
  unsubscribe();
});

it('does not publish an old poll failure after a newer verification', async () => {
  vi.useFakeTimers();
  let rejectPoll: (error: Error) => void = () => {};
  const pending = new Promise<Response>((_resolve, reject) => {
    rejectPoll = reject;
  });
  const starting = {
    hostRunId: 'host',
    revision: 1,
    connectionRevision: 1,
    diagnosticsDegraded: false,
    handshake: null,
    status: 'starting',
  };
  const request = vi
    .fn<typeof fetch>()
    .mockReturnValueOnce(pending)
    .mockResolvedValueOnce(Response.json(starting));
  const { bridge } = createBrowserBridge('proof', channel, request);
  const onError = vi.fn();
  const unsubscribe = bridge.subscribe(vi.fn(), onError);
  await vi.advanceTimersByTimeAsync(1500);
  await bridge.verify();
  rejectPoll(new Error('old failure'));
  await vi.advanceTimersByTimeAsync(0);
  expect(onError).not.toHaveBeenCalled();
  unsubscribe();
});

it('aborts an overdue wait and never produces a successful notice', async () => {
  vi.useFakeTimers();
  let cancelled = false;
  const request = vi.fn<typeof fetch>(async (_input, options) => {
    return await new Promise<Response>((_resolve, reject) => {
      options?.signal?.addEventListener('abort', () => {
        cancelled = true;
        reject(new Error('aborted'));
      });
    });
  });
  const { session } = createBrowserBridge('proof', channel, request);
  const waiting = session.waitConnection?.(1, 2);
  if (!waiting) throw new Error('wait unavailable');
  const result = waiting.catch((error: unknown) => error);
  await vi.advanceTimersByTimeAsync(4000);
  expect(await result).toBeInstanceOf(Error);
  expect(cancelled).toBe(true);
  expect(request).toHaveBeenCalledOnce();
});
