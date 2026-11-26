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
  lowerPressureLimitPa: 0,
  upperPressureLimitPa: 1100,
  lowerPressureHoldSeconds: 0,
  upperPressureHoldSeconds: 2,
};

function Form({
  submit,
  parameters = initial,
}: {
  submit: (value: TestParameters) => void;
  parameters?: TestParameters;
}) {
  const [draft, setDraft] = useState(() => draftFrom(parameters));
  return (
    <ParametersScreen
      initial={parameters}
      draft={draft}
      setDraft={setDraft}
      live
      onBack={() => {}}
      onContinue={submit}
    />
  );
}

it('requires all four pressure fields and accepts the inclusive bounds and zero hold', async () => {
  const {
    lowerPressureLimitPa,
    upperPressureLimitPa,
    lowerPressureHoldSeconds,
    upperPressureHoldSeconds,
    ...withoutLimits
  } = initial;
  const submit = vi.fn();
  render(<Form submit={submit} parameters={withoutLimits} />);
  const user = userEvent.setup();
  const lower = screen.getByLabelText('Нижний аварийный предел, Па');
  const upper = screen.getByLabelText('Верхний аварийный предел, Па');
  const lowerHold = screen.getByLabelText('Выдержка ниже нижнего предела, с');
  const upperHold = screen.getByLabelText('Выдержка выше верхнего предела, с');
  const button = screen.getByRole('button', { name: /Проверить готовность/ });
  for (const field of [lower, upper, lowerHold, upperHold])
    expect(field).toHaveProperty('value', '');
  await user.click(button);
  expect(submit).not.toHaveBeenCalled();
  for (const field of [lower, upper, lowerHold, upperHold])
    expect(field.getAttribute('aria-invalid')).toBe('true');
  await user.type(lower, String(lowerPressureLimitPa));
  await user.type(upper, String(upperPressureLimitPa));
  await user.type(lowerHold, String(lowerPressureHoldSeconds));
  await user.type(upperHold, String(upperPressureHoldSeconds));
  await user.click(button);
  expect(submit).toHaveBeenCalledWith(
    expect.objectContaining({
      lowerPressureLimitPa,
      upperPressureLimitPa,
      lowerPressureHoldSeconds,
      upperPressureHoldSeconds,
    }),
  );
});

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

it('gives each pressure limit field its own help without submitting the form', async () => {
  const submit = vi.fn();
  render(<Form submit={submit} />);
  const user = userEvent.setup();
  const lowerLabel = 'Нижний аварийный предел, Па';
  const help = screen.getByRole('button', { name: `Подсказка к полю «${lowerLabel}»` });
  expect(
    screen.queryByRole('button', { name: 'Подсказка об аварийных пределах давления' }),
  ).toBeNull();
  expect(screen.queryByRole('tooltip')).toBeNull();

  for (const [label, example] of [
    [lowerLabel, '950 Па'],
    ['Выдержка ниже нижнего предела, с', '3 с подряд'],
    ['Верхний аварийный предел, Па', '1 050 Па'],
    ['Выдержка выше верхнего предела, с', '2 с подряд'],
  ]) {
    const fieldHelp = screen.getByRole('button', { name: `Подсказка к полю «${label}»` });
    await user.hover(fieldHelp);
    expect((await screen.findByRole('tooltip')).textContent).toContain(example);
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('tooltip')).toBeNull();
    await user.unhover(fieldHelp);
  }

  await user.click(screen.getByText(lowerLabel, { exact: true }));
  expect(document.activeElement).toBe(screen.getByRole('textbox', { name: lowerLabel }));
  await user.tab({ shift: true });
  expect(document.activeElement).toBe(help);
  expect((await screen.findByRole('tooltip')).textContent).toContain('950 Па');
  await user.keyboard('{Escape}');
  expect(screen.queryByRole('tooltip')).toBeNull();
  expect(document.activeElement).toBe(help);
  await user.keyboard('{Enter}');
  expect(submit).not.toHaveBeenCalled();
});

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
      stop={null}
      onReturn={() => undefined}
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
  const retries = screen.getByLabelText('Число повторов для каждой стабилизации');
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
