import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { expect, it, vi } from 'vitest';
import { draftFrom, ParametersScreen } from '../src/components/OtherScreens';
import type { TestParameters } from '../src/model';

const initial: TestParameters = {
  id: '',
  valve: 'К-1',
  area: 1,
  pmax: 1000,
  direction: 'pressure',
  actuator: false,
  voltage: 24,
  openSeconds: 1,
  closeSeconds: 1,
  measurementSeconds: 10,
};

function Form({ submit }: { submit: (value: TestParameters) => void }) {
  const [draft, setDraft] = useState(() => draftFrom(initial));
  return (
    <ParametersScreen
      initial={initial}
      draft={draft}
      setDraft={setDraft}
      live
      onBack={() => {}}
      onContinue={submit}
    />
  );
}

it('keeps the original lossy input and prevents a demo command', async () => {
  const submit = vi.fn();
  render(<Form submit={submit} />);
  const user = userEvent.setup();
  const field = screen.getByLabelText('Площадь клапана, квадратные метры');
  await user.clear(field);
  await user.type(field, '1.0000000000000001');
  await user.click(screen.getByRole('button', { name: /Проверить готовность/ }));
  expect(submit).not.toHaveBeenCalled();
  expect(field.getAttribute('aria-invalid')).toBe('true');
  expect(field).toHaveProperty('value', '1.0000000000000001');
  await user.clear(field);
  await user.type(field, '0,10');
  await user.click(screen.getByRole('button', { name: /Проверить готовность/ }));
  expect(submit).toHaveBeenCalledWith(expect.objectContaining({ area: 0.1 }));
});
