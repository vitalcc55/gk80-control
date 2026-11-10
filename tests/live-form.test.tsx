import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { expect, it, vi } from 'vitest';
import {
  ActiveParametersScreen,
  draftFrom,
  ParametersScreen,
} from '../src/components/ParametersScreen';
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
  measurementSeconds: 60,
  preOpenStabilizationSeconds: 10,
  postCloseStabilizationSeconds: 10,
  postCloseStabilizationRetries: 1,
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

function EmptyForm({ submit }: { submit: (value: TestParameters) => void }) {
  const [draft, setDraft] = useState(() =>
    draftFrom({ ...initial, postCloseStabilizationRetries: null }),
  );
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

it('edits and submits both stabilization intervals independently', async () => {
  const submit = vi.fn();
  render(<Form submit={submit} />);
  const user = userEvent.setup();
  const before = screen.getByLabelText('Стабилизация перед открытием клапана, с');
  const after = screen.getByLabelText('Стабилизация после закрытия клапана, с');
  expect(before).toHaveProperty('value', '10');
  expect(after).toHaveProperty('value', '10');
  await user.clear(before);
  await user.type(before, '13');
  await user.clear(after);
  await user.type(after, '17');
  await user.click(screen.getByRole('button', { name: /Проверить готовность/ }));
  expect(submit).toHaveBeenCalledWith(
    expect.objectContaining({ preOpenStabilizationSeconds: 13, postCloseStabilizationSeconds: 17 }),
  );
});

it('shows both saved stabilization intervals for the active test', () => {
  render(
    <ActiveParametersScreen
      parameters={{
        ...initial,
        preOpenStabilizationSeconds: 13,
        postCloseStabilizationSeconds: 17,
        postCloseStabilizationRetries: 1,
      }}
    />,
  );
  expect(
    screen.getByText('Стабилизация перед открытием клапана').nextElementSibling?.textContent,
  ).toBe('13 с');
  expect(
    screen.getByText('Стабилизация после закрытия клапана').nextElementSibling?.textContent,
  ).toBe('17 с');
});

it('requires an explicit repeat count and accepts zero at the duration boundaries', async () => {
  const submit = vi.fn();
  render(<EmptyForm submit={submit} />);
  const user = userEvent.setup();
  const retries = screen.getByLabelText('Допустимое число повторов стабилизации после закрытия');
  expect(retries).toHaveProperty('value', '');
  await user.click(screen.getByRole('button', { name: /Проверить готовность/ }));
  expect(submit).not.toHaveBeenCalled();
  await user.type(retries, '0');
  const measurement = screen.getByLabelText('Длительность измерения, с');
  await user.clear(measurement);
  await user.type(measurement, '300');
  await user.click(screen.getByRole('button', { name: /Проверить готовность/ }));
  expect(submit).toHaveBeenCalledWith(
    expect.objectContaining({ postCloseStabilizationRetries: 0, measurementSeconds: 300 }),
  );
  await user.clear(retries);
  await user.type(retries, '101');
  await user.clear(measurement);
  await user.type(measurement, '60,5');
  await user.click(screen.getByRole('button', { name: /Проверить готовность/ }));
  expect(submit).toHaveBeenCalledTimes(1);
});
