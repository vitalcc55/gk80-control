import { cleanup } from '@testing-library/react';
import { afterEach } from 'vitest';
import { resetObservationsForTests } from '../src/desktop/session-observation';

afterEach(() => {
  cleanup();
  // Each test represents a new host; unresolved calls remain owned by the old one.
  resetObservationsForTests();
});
