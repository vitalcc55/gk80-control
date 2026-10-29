import type { Events } from '@wailsio/runtime';
import { expectTypeOf, test } from 'vitest';
import type {
  ConnectionState,
  Event,
  Measurement,
  Snapshot,
} from '../src/generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';

test('generated types retain unavailable readings and typed core events', () => {
  expectTypeOf<Measurement['value']>().toEqualTypeOf<number | null>();
  expectTypeOf<Measurement['measuredAt']>().toEqualTypeOf<string | null>();
  expectTypeOf<Snapshot['activeTest']>().toEqualTypeOf<
    | import('../src/generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models').TestRecord
    | null
  >();
  expectTypeOf<Events.CustomEvents['core:event']>().toEqualTypeOf<Event>();
  expectTypeOf<Events.CustomEvents['core:connection']>().toEqualTypeOf<ConnectionState>();
});
