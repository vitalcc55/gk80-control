import { afterEach, expect, it, vi } from 'vitest';
import { createBrowserBridge } from '../src/desktop/browser-bridge';
import { CommandConflictError } from '../src/desktop/demo-service';
import {
  TestParametersDirection,
  TestRecordOutcome,
} from '../src/generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import responseExamples from './fixtures/browser-responses.json';

afterEach(() => vi.useRealTimers());

const channel = { generation: 'channel-generation', signal: new AbortController().signal };

it('rejects readiness with matching identity but missing readiness facts', async () => {
  const fetcher = vi.fn<typeof fetch>().mockResolvedValue(
    Response.json({
      testId: '10000000-0000-4000-8000-000000000003',
      revision: 0,
    }),
  );
  await expect(
    createBrowserBridge(channel, fetcher).service.readiness('10000000-0000-4000-8000-000000000003'),
  ).rejects.toThrow();
  expect(fetcher).toHaveBeenCalledOnce();
});
const responseId = '10000000-0000-4000-8000-000000000001';

it.each(['invalid UTF-8', 'leading BOM'])(
  'rejects a command receipt with %s bytes',
  async (kind) => {
    const receipt = `{"delivery":"response","command":{"commandId":"${responseId}","status":"completed","reason":"`;
    const bytes =
      kind === 'invalid UTF-8'
        ? new Uint8Array([...new TextEncoder().encode(receipt), 0xff, 34, 125, 125])
        : new Uint8Array([0xef, 0xbb, 0xbf, ...new TextEncoder().encode(`${receipt}ok"}}`)]);
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(bytes));
    const service = createBrowserBridge(channel, fetcher).service;
    await expect(service.stopTest(responseId, { commandId: responseId })).rejects.toThrow();
    expect(fetcher).toHaveBeenCalledOnce();
  },
);

type Browser = ReturnType<typeof createBrowserBridge>;

it.each(['outcome', 'createdFrom', 'createdBefore'] as const)(
  'does not silently remove an invalid empty %s filter',
  async (field) => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response('{}', { status: 400 }));
    const service = createBrowserBridge(channel, fetcher).service;
    await expect(service.listTests({ [field]: '' })).rejects.toThrow();
    const query = new URL(String(fetcher.mock.calls[0]?.[0]), 'http://127.0.0.1:17880')
      .searchParams;
    expect(query.has(field)).toBe(true);
    expect(query.get(field)).toBe('');
  },
);

it('passes history filters and cursor together to the bounded read operation', async () => {
  const fetcher = vi
    .fn<typeof fetch>()
    .mockResolvedValue(Response.json({ items: [], nextAfter: null }));
  const service = createBrowserBridge(channel, fetcher).service;
  await service.listTests({
    q: 'Клапан 100%_',
    createdFrom: '2026-11-05T00:00:00+03:00',
    createdBefore: '2026-11-06T00:00:00+03:00',
    outcome: TestRecordOutcome.TestRecordOutcomeCompleted,
    after: responseId,
    limit: 20,
  });
  const query = new URL(String(fetcher.mock.calls[0]?.[0]), 'http://127.0.0.1:17880').searchParams;
  expect(Object.fromEntries(query)).toEqual({
    q: 'Клапан 100%_',
    createdFrom: '2026-11-05T00:00:00+03:00',
    createdBefore: '2026-11-06T00:00:00+03:00',
    outcome: 'completed',
    after: responseId,
    limit: '20',
  });
  expect(fetcher).toHaveBeenCalledOnce();
});
const operations: { name: string; schema: string; call: (browser: Browser) => Promise<unknown> }[] =
  [
    { name: 'status', schema: 'DesktopStatus', call: (b) => b.bridge.status() },
    { name: 'verify', schema: 'DesktopStatus', call: (b) => b.bridge.verify() },
    {
      name: 'session ticket',
      schema: 'BrowserSessionTicket',
      call: (b) => b.session.beginSessionCheck(),
    },
    { name: 'session view', schema: 'SessionView', call: (b) => b.session.sessionView() },
    {
      name: 'passive session',
      schema: 'SessionView',
      call: (b) => {
        if (!b.session.observeSession) throw new Error('Missing observeSession');
        return b.session.observeSession();
      },
    },
    {
      name: 'notice',
      schema: 'ConnectionNotice',
      call: (b) => {
        if (!b.session.waitConnection) throw new Error('Missing waitConnection');
        return b.session.waitConnection(0, 0);
      },
    },
    {
      name: 'create',
      schema: 'CommandDispatch',
      call: (b) =>
        b.service.createTest({
          commandId: responseId,
          expectedRevision: 0,
          parameters: {
            valve: 'V',
            areaM2: 0.1,
            pmaxPa: 1000,
            measurementSeconds: 1,
            direction: TestParametersDirection.TestParametersDirectionPressure,
            actuator: false,
          },
        }),
    },
    {
      name: 'start',
      schema: 'CommandDispatch',
      call: (b) =>
        b.service.startTest(responseId, {
          commandId: responseId,
          expectedRevision: 0,
          valveClosed: true,
          connectionConfirmed: true,
        }),
    },
    {
      name: 'confirm',
      schema: 'CommandDispatch',
      call: (b) =>
        b.service.confirmAction(responseId, responseId, {
          commandId: responseId,
          expectedRevision: 0,
        }),
    },
    {
      name: 'stop',
      schema: 'CommandDispatch',
      call: (b) => b.service.stopTest(responseId, { commandId: responseId }),
    },
    { name: 'readiness', schema: 'Readiness', call: (b) => b.service.readiness(responseId) },
    { name: 'command read', schema: 'CommandStatus', call: (b) => b.service.command(responseId) },
    {
      name: 'exact lookup',
      schema: 'ExactLookupResult',
      call: (b) =>
        b.service.lookupCommandExact(responseId, 'stop', responseId, '', responseId, '{}'),
    },
    { name: 'history list', schema: 'TestList', call: (b) => b.service.listTests({}) },
    { name: 'history record', schema: 'TestRecord', call: (b) => b.service.test(responseId) },
    {
      name: 'measurement page',
      schema: 'MeasurementPage',
      call: (b) => b.service.measurements(responseId, 0, 20),
    },
    { name: 'audit page', schema: 'AuditPage', call: (b) => b.service.audit(responseId, 0, 20) },
  ];

function completeResponse(schema: string): string {
  const example = responseExamples.find(
    (item) => item.schema === schema && item.name === `complete ${schema}`,
  );
  if (!example) throw new Error(`Missing complete example: ${schema}`);
  return example.json;
}

it.each(operations)(
  'checks schema and HTTP status before delivering $name',
  async ({ schema, call }) => {
    for (const [body, status, valid] of [
      [completeResponse(schema), 200, true],
      ['{}', 200, false],
      [completeResponse(schema), 202, false],
    ] as const) {
      const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(body, { status }));
      const result = call(createBrowserBridge(channel, fetcher));
      if (valid) await expect(result).resolves.toBeDefined();
      else await expect(result).rejects.toBeInstanceOf(Error);
      expect(fetcher).toHaveBeenCalledOnce();
    }
  },
);

it('accepts only the no-body heartbeat status', async () => {
  for (const status of [204, 200, 202]) {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(null, { status }));
    const result = createBrowserBridge(channel, fetcher).session.keepAlive(responseId);
    if (status === 204) await expect(result).resolves.toBeUndefined();
    else await expect(result).rejects.toThrow();
    expect(fetcher).toHaveBeenCalledOnce();
  }
});

it.each(['status', 'exact', 'heartbeat'] as const)(
  'rejects a late %s body after channel closure',
  async (kind) => {
    const owner = new AbortController();
    let finish: (value: string) => void = () => {};
    let started: () => void = () => {};
    const reading = new Promise<void>((resolve) => {
      started = resolve;
    });
    const body = new Promise<string>((resolve) => {
      finish = resolve;
    });
    const response = new Response(null, { status: kind === 'heartbeat' ? 204 : 200 });
    vi.spyOn(response, 'arrayBuffer').mockImplementation(() => {
      started();
      return body.then((text) => new TextEncoder().encode(text).buffer);
    });
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(response);
    const browser = createBrowserBridge({ generation: 'original', signal: owner.signal }, fetcher);
    const pending = (
      kind === 'status'
        ? browser.bridge.status()
        : kind === 'heartbeat'
          ? browser.session.keepAlive(responseId)
          : browser.service.lookupCommandExact(responseId, 'stop', responseId, '', responseId, '{}')
    ).catch((error: unknown) => error);
    await reading;
    owner.abort();
    finish(
      kind === 'heartbeat'
        ? ''
        : kind === 'status'
          ? completeResponse('DesktopStatus')
          : completeResponse('ExactLookupResult'),
    );
    expect(await pending).toBeInstanceOf(Error);
    expect(fetcher).toHaveBeenCalledOnce();
  },
);

it('rejects raw duplicate exact results without changing the original lookup JSON or retrying', async () => {
  const original = '{ "commandId" : "10000000-0000-4000-8000-000000000001" }';
  for (const body of [
    '{"outcome":"unknown","outcome":"conflict","command":null}',
    '{"outcome":"unknown","out\\u0063ome":"conflict","command":null}',
    `{"outcome":"match","command":{"commandId":"${responseId}","status":"unknown","status":"completed"}}`,
  ]) {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(body));
    const lookup = createBrowserBridge(channel, fetcher).service.lookupCommandExact(
      responseId,
      'stop',
      responseId,
      '',
      responseId,
      original,
    );
    const error: unknown = await lookup.catch((value: unknown) => value);
    expect(error).toBeInstanceOf(Error);
    expect(error).not.toBeInstanceOf(CommandConflictError);
    expect(fetcher).toHaveBeenCalledOnce();
    expect(fetcher.mock.calls[0]?.[1]?.body).toBe(original);
  }
});

it('keeps a valid exact conflict distinct from a direct confirmed conflict', async () => {
  const fetcher = vi
    .fn<typeof fetch>()
    .mockResolvedValueOnce(Response.json({ outcome: 'conflict', command: null }))
    .mockResolvedValueOnce(Response.json({ delivery: 'conflict', command: null }));
  const browser = createBrowserBridge(channel, fetcher);
  await expect(
    browser.service.lookupCommandExact(responseId, 'stop', responseId, '', responseId, '{}'),
  ).resolves.toEqual({ outcome: 'conflict', command: null });
  await expect(
    browser.service.stopTest(responseId, { commandId: responseId }),
  ).rejects.toBeInstanceOf(CommandConflictError);
  expect(fetcher).toHaveBeenCalledTimes(2);
});

it('rejects null measurements inside an otherwise complete snapshot', async () => {
  const request = vi.fn<typeof fetch>().mockResolvedValue(
    Response.json({
      connection: {
        hostRunId: '10000000-0000-4000-8000-000000000001',
        revision: 0,
        streamGeneration: 0,
        status: 'live',
        linkValid: true,
        leaseAcquireUnknown: false,
      },
      snapshot: {
        apiVersion: 'v1',
        backendRunId: '10000000-0000-4000-8000-000000000002',
        revision: 0,
        lastEventSequence: 0,
        activeTest: null,
        pendingAction: null,
        measurements: null,
      },
      validForMs: 1000,
      linkValidForMs: 1000,
    }),
  );
  await expect(createBrowserBridge(channel, request).session.sessionView()).rejects.toThrow();
  expect(request).toHaveBeenCalledOnce();
});

it('rejects an empty readiness response at the real browser adapter', async () => {
  const request = vi.fn<typeof fetch>().mockResolvedValue(Response.json({}));
  await expect(
    createBrowserBridge(channel, request).service.readiness('10000000-0000-4000-8000-000000000003'),
  ).rejects.toThrow();
  expect(request).toHaveBeenCalledOnce();
});

it('rejects a closed channel before dispatching any HTTP operation', async () => {
  const owner = new AbortController();
  owner.abort();
  const request = vi.fn<typeof fetch>();
  const { service } = createBrowserBridge({ generation: 'closed', signal: owner.signal }, request);
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
  const { service } = createBrowserBridge({ generation: 'first', signal: owner.signal }, request);
  const outcome = service
    .stopTest('test', { commandId: 'command' })
    .catch((error: unknown) => error);
  owner.abort();
  finish(Response.json({ delivery: 'conflict', command: null }));
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
    { generation: 'first', signal: owner.signal },
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
  let finishBody: (text: string) => void = () => {};
  let bodyStarted: () => void = () => {};
  const started = new Promise<void>((resolve) => {
    bodyStarted = resolve;
  });
  const body = new Promise<string>((resolve) => {
    finishBody = resolve;
  });
  const response = new Response();
  vi.spyOn(response, 'arrayBuffer').mockImplementation(() => {
    bodyStarted();
    return body.then((text) => new TextEncoder().encode(text).buffer);
  });
  const request = vi.fn<typeof fetch>().mockResolvedValue(response);
  const { service } = createBrowserBridge({ generation: 'first', signal: owner.signal }, request);
  const outcome = service
    .stopTest('test', { commandId: 'command' })
    .catch((error: unknown) => error);
  await started;
  owner.abort();
  finishBody('{"delivery":"conflict","command":null}');
  const error = await outcome;
  expect(error).toBeInstanceOf(Error);
  expect(error).not.toBeInstanceOf(CommandConflictError);
  expect(request).toHaveBeenCalledOnce();
});

it('uses a new generation after reconnect and rejects an old channel response', async () => {
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
    { generation: 'old-channel', signal: first.signal },
    request,
  );
  const old = oldBridge.service
    .lookupCommandExact('database', 'stop', 'test', '', 'command', '{}')
    .catch((error: unknown) => error);
  first.abort();
  const second = new AbortController();
  const newBridge = createBrowserBridge(
    { generation: 'new-channel', signal: second.signal },
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

it('sends the original Stop attempt and connection generation in the header', async () => {
  const request = vi.fn<typeof fetch>(async () =>
    Response.json({ outcome: 'unknown', command: null }),
  );
  const { service } = createBrowserBridge(channel, request);
  const original = '{ "commandId" : "command" }';
  await service.lookupCommandExact('database', 'stop', 'test', '', 'command', original);
  expect(request).toHaveBeenCalledOnce();
  const [path, options] = request.mock.calls[0] ?? [];
  expect(path).toBe('/api/commands/command/lookup-exact');
  expect(options?.body).toBe(original);
  expect(options?.redirect).toBe('error');
  const headers = new Headers(options?.headers);
  expect(headers.has('X-GK80-Session')).toBe(false);
  expect(headers.get('X-GK80-Channel')).toBe('channel-generation');
  expect(headers.get('X-GK80-Database')).toBe('database');
  expect(headers.get('X-GK80-Kind')).toBe('stop');
  expect(headers.get('X-GK80-Test')).toBe('test');
  expect(headers.has('X-GK80-Action')).toBe(false);
});

it('distinguishes a confirmed command conflict and never retries a failed POST', async () => {
  const request = vi.fn<typeof fetch>();
  request.mockResolvedValueOnce(Response.json({ delivery: 'conflict', command: null }));
  request.mockResolvedValueOnce(Response.json({ error: 'command_conflict' }, { status: 409 }));
  request.mockResolvedValueOnce(
    new Response('{"error":"other","error":"command_conflict"}', { status: 409 }),
  );
  request.mockRejectedValueOnce(new Error('connection lost'));
  const { service } = createBrowserBridge(channel, request);
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
        hostRunId: '10000000-0000-4000-8000-000000000001',
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
        hostRunId: '10000000-0000-4000-8000-000000000001',
        revision: 1,
        streamGeneration: 1,
        status: 'live',
        linkValid: true,
        leaseAcquireUnknown: false,
      },
      snapshot: {
        apiVersion: 'v1',
        backendRunId: '10000000-0000-4000-8000-000000000002',
        revision: 1,
        lastEventSequence: 1,
        activeTest: null,
        pendingAction: null,
        measurementTiming: null,
        measurements: [null],
      },
      validForMs: 1000,
      linkValidForMs: 1000,
    }),
  );
  const { session } = createBrowserBridge(channel, request);
  await expect(session.sessionView()).rejects.toThrow();
  await expect(session.sessionView()).rejects.toThrow();
  await expect(session.sessionView()).rejects.toThrow();
});

it('rejects a non-string desktop status', async () => {
  const request = vi.fn<typeof fetch>().mockResolvedValue(
    Response.json({
      hostRunId: '10000000-0000-4000-8000-000000000001',
      revision: 1,
      connectionRevision: 1,
      diagnosticsDegraded: false,
      handshake: null,
      status: ['ready'],
    }),
  );
  await expect(createBrowserBridge(channel, request).bridge.status()).rejects.toThrow();
});

it('accepts a complete session snapshot with a simulated measurement', async () => {
  const snapshot = {
    apiVersion: 'v1',
    backendRunId: '10000000-0000-4000-8000-000000000002',
    revision: 1,
    lastEventSequence: 2,
    activeTest: {
      testId: '10000000-0000-4000-8000-000000000003',
      stopCommandId: '10000000-0000-4000-8000-000000000004',
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
    measurementTiming: null,
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
      hostRunId: '10000000-0000-4000-8000-000000000001',
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
  await expect(createBrowserBridge(channel, request).session.sessionView()).resolves.toEqual(
    expected,
  );
});

it('reports failed status polling and stops on unsubscribe', async () => {
  vi.useFakeTimers();
  const request = vi.fn<typeof fetch>().mockRejectedValue(new Error('unavailable'));
  const { bridge } = createBrowserBridge(channel, request);
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
  const { bridge } = createBrowserBridge({ generation: 'first', signal: owner.signal }, request);
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
    hostRunId: '10000000-0000-4000-8000-000000000001',
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
  const { bridge } = createBrowserBridge(channel, request);
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
  const { session } = createBrowserBridge(channel, request);
  const waiting = session.waitConnection?.(1, 2);
  if (!waiting) throw new Error('wait unavailable');
  const result = waiting.catch((error: unknown) => error);
  await vi.advanceTimersByTimeAsync(4000);
  expect(await result).toBeInstanceOf(Error);
  expect(cancelled).toBe(true);
  expect(request).toHaveBeenCalledOnce();
});

it('rejects ambiguous command envelopes and unexpected success statuses', async () => {
  const request = vi
    .fn<typeof fetch>()
    .mockResolvedValueOnce(
      new Response('{"delivery":"response","delivery":"conflict","command":null}'),
    )
    .mockResolvedValueOnce(new Response('{"delivery":"conflict","command":null,"command":null}'))
    .mockResolvedValueOnce(Response.json({ delivery: 'conflict', command: null }, { status: 202 }));
  const { service } = createBrowserBridge(channel, request);
  for (let index = 0; index < 3; index++) {
    const error: unknown = await service
      .stopTest('test', { commandId: 'command' })
      .catch((failure: unknown) => failure);
    expect(error).toBeInstanceOf(Error);
    expect(error).not.toBeInstanceOf(CommandConflictError);
  }
  expect(request).toHaveBeenCalledTimes(3);
});
