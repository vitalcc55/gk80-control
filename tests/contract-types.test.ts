// @vitest-environment node
import type { Events } from '@wailsio/runtime';
import { expect, expectTypeOf, test } from 'vitest';
import { parseBrowserJSON, responseGuards } from '../src/desktop/browser-responses';
import type {
  ConnectionState,
  Event,
  Handshake,
  Measurement,
  Snapshot,
} from '../src/generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';
import examples from './fixtures/browser-responses.json';

test('generated types retain unavailable readings and typed core events', () => {
  expectTypeOf<Handshake['databaseId']>().toEqualTypeOf<string>();
  expectTypeOf<Measurement['value']>().toEqualTypeOf<number | null>();
  expectTypeOf<Measurement['measuredAt']>().toEqualTypeOf<string | null>();
  expectTypeOf<Snapshot['activeTest']>().toEqualTypeOf<
    | import('../src/generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models').TestRecord
    | null
  >();
  expectTypeOf<Events.CustomEvents['core:event']>().toEqualTypeOf<Event>();
  expectTypeOf<Events.CustomEvents['core:connection']>().toEqualTypeOf<ConnectionState>();
});

const guards: Readonly<Record<string, (value: unknown) => boolean>> = responseGuards;

test('history rejects a total smaller than its returned page', () => {
  const example = examples.find((value) => value.name === 'nonempty history');
  if (!example) throw new Error('Missing history example');
  const page = parseBrowserJSON(example.json);
  if (typeof page !== 'object' || page === null) throw new Error('Invalid history example');
  expect(responseGuards.TestList({ ...page, total: 0 })).toBe(false);
  expect(
    responseGuards.TestList({
      ...page,
      total: 1,
      nextAfter: '10000000-0000-4000-8000-000000000001',
    }),
  ).toBe(false);
});

for (const example of examples) {
  test(`${example.schema}: ${example.name}`, () => {
    const guard = guards[example.schema];
    if (!guard) throw new Error(`Missing response guard: ${example.schema}`);
    let accepted = false;
    try {
      accepted = guard(parseBrowserJSON(example.json));
    } catch {
      accepted = false;
    }
    expect(accepted).toBe(example.browserValid);
    if (example.kind !== 'javascript') expect(example.browserValid).toBe(example.valid);
  });
}

test('each response guard has schema-derived positive and negative examples', () => {
  for (const schema of Object.keys(responseGuards)) {
    const cases = examples.filter(
      (example) => example.schema === schema && example.kind === 'schema',
    );
    expect(
      cases.some((example) => example.valid),
      schema,
    ).toBe(true);
    expect(
      cases.some((example) => !example.valid),
      schema,
    ).toBe(true);
  }
});
