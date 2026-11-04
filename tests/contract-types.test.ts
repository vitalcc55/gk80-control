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
