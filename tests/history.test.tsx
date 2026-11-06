import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';

vi.mock('../src/components/MeasurementChart', () => ({ MeasurementChart: () => null }));

import { type ReactNode, useState } from 'react';
import { AppShell } from '../src/components/AppShell';
import type { SessionBridge } from '../src/desktop/bridge';
import { History } from '../src/desktop/History';
import { eventActionLabel, eventOutcomeLabel, eventReasonLabels } from '../src/desktop/display';
import { LiveApp } from '../src/desktop/LiveApp';
import { RecordDetails } from '../src/desktop/RecordDetails';
import { desktopDemoService } from '../src/desktop/wails-bridge';
import { FixtureTests } from '../src/FixtureTests';
import { fixtureSource } from '../src/fixtureAdapter';
import {
  ConnectionStateStatus,
  MeasurementChannel,
  type MeasurementPage,
  MeasurementQuality,
  MeasurementQuantity,
  MeasurementSource,
  MeasurementUnit,
  type TestList,
  TestParametersDirection,
  type TestRecord,
  TestRecordDataQuality,
  TestRecordOutcome,
  TestRecordSaveStatus,
  TestRecordStage,
} from '../src/generated/wails/github.com/vitalcc55/gk80-control/internal/contract/models';

const record: TestRecord = {
  testId: '33333333-3333-4333-8333-333333333333',
  stopCommandId: '55555555-5555-4555-8555-555555555555',
  createdAt: '2026-11-05T00:00:00Z',
  operatorId: 'demo',
  demo: true,
  stage: TestRecordStage.Finished,
  outcome: TestRecordOutcome.TestRecordOutcomeCompleted,
  saveStatus: TestRecordSaveStatus.TestRecordSaveStatusSaved,
  dataQuality: TestRecordDataQuality.TestRecordDataQualityComplete,
  parameters: {
    valve: 'Первая запись',
    areaM2: 0.012,
    pmaxPa: 10000,
    measurementSeconds: 30,
    direction: TestParametersDirection.TestParametersDirectionPressure,
    actuator: false,
  },
};
const callbacks = { onNew: vi.fn(), onCopy: vi.fn(), onDiscard: vi.fn() };

function measurementPage(nextAfter: number | null): MeasurementPage {
  const sequence = nextAfter ?? 201;
  return {
    items: [{
      sequence,
      channel: MeasurementChannel.Dd1,
      measuredAt: record.createdAt,
      observedAt: record.createdAt,
      quality: MeasurementQuality.MeasurementQualityValid,
      quantity: MeasurementQuantity.MeasurementQuantityPressure,
      source: MeasurementSource.MeasurementSourceSimulator,
      unit: MeasurementUnit.Pa,
      value: sequence,
    }],
    nextAfter,
  };
}

it('limits the prepared history page including the active record', async () => {
  const entries = Array.from({ length: 21 }, (_, index) => ({
    ...fixtureSource.initial,
    id: `record-${index}`,
    valve: `Клапан ${index}`,
  }));
  function PreparedHistory() {
    const [size, setSize] = useState(20);
    return (
      <FixtureTests
        pageSize={size}
        onPageSize={setSize}
        empty={false}
        showExample={false}
        draftAvailable={false}
        activeParameters={{ ...fixtureSource.initial, id: 'active', valve: 'Активный клапан' }}
        history={entries}
        example={fixtureSource.initial}
        onNew={() => {}}
        onHistory={() => {}}
        onCurrent={() => {}}
      />
    );
  }
  render(<PreparedHistory />);
  expect(screen.getAllByRole('row')).toHaveLength(21);
  expect(screen.getByRole('button', { name: /Вернуться:.*Активный клапан/ })).toBeTruthy();
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: 'Следующая страница' }));
  expect(screen.getAllByRole('row')).toHaveLength(3);
  await user.selectOptions(screen.getByLabelText('Записей на странице'), '10');
  expect(screen.getAllByRole('row')).toHaveLength(11);
  expect(screen.getByRole('button', { name: /Вернуться:.*Активный клапан/ })).toBeTruthy();
  await user.type(screen.getByLabelText('С даты'), '2026-11-10');
  await user.type(screen.getByLabelText('По дату'), '2026-11-05');
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  expect(screen.getByRole('alert').textContent).toMatch(/Начало диапазона/);
  expect(screen.queryByRole('button', { name: 'Следующая страница' })).toBeNull();
});

it('keeps the applied search when page size changes offline while an old page is pending', async () => {
  let finishOld: (value: TestList) => void = () => {};
  const oldPage = new Promise<TestList>((resolve) => {
    finishOld = resolve;
  });
  const listTests = vi
    .fn()
    .mockResolvedValueOnce({ items: [record], nextAfter: record.testId })
    .mockResolvedValueOnce({ items: [record], nextAfter: record.testId })
    .mockReturnValueOnce(oldPage)
    .mockResolvedValue({ items: [], nextAfter: null });
  const service = { ...desktopDemoService, listTests };
  const draw = (available: boolean) => (
    <History
      {...callbacks}
      service={service}
      available={available}
      disabled={false}
      draftAvailable={false}
    />
  );
  const view = render(draw(true));
  const user = userEvent.setup();
  await screen.findByRole('button', { name: /Открыть Первая/ });
  const search = screen.getByLabelText('Поиск по номеру или изделию');
  await user.type(search, 'Применено');
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  await user.click(await screen.findByRole('button', { name: 'Следующая страница' }));
  view.rerender(draw(false));
  await user.clear(search);
  await user.type(search, 'Черновик');
  await user.selectOptions(screen.getByLabelText('Записей на странице'), '50');
  await act(async () => finishOld({ items: [record], nextAfter: null }));
  expect(screen.queryByRole('button', { name: /Открыть Первая/ })).toBeNull();
  expect(listTests).toHaveBeenCalledTimes(3);
  view.rerender(draw(true));
  await waitFor(() =>
    expect(listTests).toHaveBeenLastCalledWith({ q: 'Применено', limit: 50, after: null }),
  );
  expect(listTests).toHaveBeenCalledTimes(4);
  expect(search).toHaveProperty('value', 'Черновик');
});

it('changes page size without applying an unfinished search or accepting an old page', async () => {
  let finishOld: (value: TestList) => void = () => {};
  const oldPage = new Promise<TestList>((resolve) => {
    finishOld = resolve;
  });
  const listTests = vi
    .fn()
    .mockResolvedValueOnce({ items: [record], nextAfter: record.testId })
    .mockResolvedValueOnce({ items: [record], nextAfter: record.testId })
    .mockReturnValueOnce(oldPage)
    .mockResolvedValue({ items: [], nextAfter: null });
  const service = { ...desktopDemoService, listTests };
  render(
    <History {...callbacks} service={service} available disabled={false} draftAvailable={false} />,
  );
  const user = userEvent.setup();
  expect(await screen.findByRole('button', { name: /Открыть Первая/ })).toBeTruthy();
  expect(listTests).toHaveBeenLastCalledWith({ limit: 20, after: null });
  const search = screen.getByLabelText('Поиск по номеру или изделию');
  await user.type(search, 'Применено');
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  await user.click(await screen.findByRole('button', { name: 'Следующая страница' }));
  await user.clear(search);
  await user.type(search, 'Не отправлено');
  await user.selectOptions(screen.getByLabelText('Записей на странице'), '10');
  await waitFor(() =>
    expect(listTests).toHaveBeenLastCalledWith({ q: 'Применено', limit: 10, after: null }),
  );
  await act(async () => finishOld({ items: [record], nextAfter: null }));
  expect(screen.queryByRole('button', { name: /Открыть Первая/ })).toBeNull();
  expect(search).toHaveProperty('value', 'Не отправлено');
});

it('normalizes history whitespace like the core without removing a byte order mark', async () => {
  const service = {
    ...desktopDemoService,
    listTests: vi.fn().mockResolvedValue({ items: [], nextAfter: null }),
  };
  render(
    <History {...callbacks} service={service} available disabled={false} draftAvailable={false} />,
  );
  const user = userEvent.setup();
  const input = screen.getByLabelText('Поиск по номеру или изделию');
  await user.click(input);
  await user.paste('\ufeff');
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  await waitFor(() =>
    expect(service.listTests).toHaveBeenLastCalledWith(expect.objectContaining({ q: '\ufeff' })),
  );
  await user.clear(input);
  const name = 'А'.repeat(200);
  await user.paste(`\u0085${name}\u0085`);
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  await waitFor(() =>
    expect(service.listTests).toHaveBeenLastCalledWith(expect.objectContaining({ q: name })),
  );
});

it('distinguishes the stored record from an unsaved latest result in the list and detail', async () => {
  const stored = {
    ...record,
    stage: TestRecordStage.Running,
    outcome: TestRecordOutcome.TestRecordOutcomePending,
    dataQuality: TestRecordDataQuality.TestRecordDataQualityPending,
  };
  const latest = {
    ...record,
    outcome: TestRecordOutcome.TestRecordOutcomeInterrupted,
    dataQuality: TestRecordDataQuality.TestRecordDataQualityIncomplete,
    saveStatus: TestRecordSaveStatus.TestRecordSaveStatusFailed,
  };
  const service = {
    ...desktopDemoService,
    listTests: vi.fn().mockResolvedValue({ items: [stored], nextAfter: null }),
    test: vi.fn().mockResolvedValue(stored),
  };
  const draw = (completedRecord: TestRecord) => (
    <History
      {...callbacks}
      service={service}
      available
      disabled={false}
      draftAvailable={false}
      completedRecord={completedRecord}
    />
  );
  const view = render(draw(latest));
  expect(
    await screen.findByText(
      /По последнему снимку: Прервано; данные: Неполные; сохранение: Ошибка сохранения/,
    ),
  ).toBeTruthy();
  await userEvent.setup().click(screen.getByRole('button', { name: /Открыть Первая/ }));
  expect(await screen.findByText(/В истории сохранена другая версия/)).toBeTruthy();
  expect(
    screen.getByText(
      /По последнему снимку: Прервано; данные: Неполные; сохранение: Ошибка сохранения/,
    ),
  ).toBeTruthy();
  expect(screen.getByText('Ожидается')).toBeTruthy();
  expect(screen.getByText('Ожидаются')).toBeTruthy();
  expect(screen.getByText('Сохранено')).toBeTruthy();
  view.unmount();
  const uncertain = { ...latest, saveStatus: TestRecordSaveStatus.TestRecordSaveStatusUnknown };
  const uncertainView = render(draw(uncertain));
  await userEvent.setup().click(await screen.findByRole('button', { name: /Открыть Первая/ }));
  expect(screen.getByText(/По последнему снимку: Прервано.*Не подтверждено/)).toBeTruthy();
  const saved = { ...uncertain, saveStatus: TestRecordSaveStatus.TestRecordSaveStatusSaved };
  service.test.mockResolvedValue(saved);
  uncertainView.rerender(draw(saved));
  await waitFor(() => expect(screen.queryByText(/В истории сохранена другая версия/)).toBeNull());
  expect(screen.getByText('Прервано')).toBeTruthy();
  expect(screen.getByText('Сохранено')).toBeTruthy();
});

function FocusShell({ children }: { children: ReactNode }) {
  return (
    <AppShell
      screen="tests"
      active={false}
      draftAvailable={false}
      onTests={() => {}}
      onRun={() => {}}
      onParameters={() => {}}
    >
      {children}
    </AppShell>
  );
}

function Retained({ hidden }: { hidden: boolean }) {
  return (
    <FocusShell>
      <div hidden={hidden}>
        <button type="button">Действие</button>
      </div>
    </FocusShell>
  );
}

it('recovers focus when a history page button is removed while loading', async () => {
  const hasFocus = vi.spyOn(document, 'hasFocus').mockReturnValue(true);
  try {
    const service = {
      ...desktopDemoService,
      listTests: vi
        .fn()
        .mockResolvedValueOnce({ items: [record], nextAfter: record.testId })
        .mockReturnValue(new Promise(() => {})),
    };
    render(
      <FocusShell>
        <History
          {...callbacks}
          service={service}
          available
          disabled={false}
          draftAvailable={false}
        />
      </FocusShell>,
    );
    await userEvent
      .setup()
      .click(await screen.findByRole('button', { name: 'Следующая страница' }));
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('main')));
  } finally {
    hasFocus.mockRestore();
  }
});

it('recovers focus when current-test navigation disappears', async () => {
  const hasFocus = vi.spyOn(document, 'hasFocus').mockReturnValue(true);
  try {
    const draw = (active: boolean) => (
      <AppShell
        screen="tests"
        active={active}
        draftAvailable={false}
        onTests={() => {}}
        onRun={() => {}}
        onParameters={() => {}}
      >
        {null}
      </AppShell>
    );
    const view = render(draw(true));
    screen.getByRole('button', { name: 'Текущее испытание' }).focus();
    view.rerender(draw(false));
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('main')));
  } finally {
    hasFocus.mockRestore();
  }
});

it('recovers focus when one fixture history row disappears but another remains', async () => {
  const hasFocus = vi.spyOn(document, 'hasFocus').mockReturnValue(true);
  try {
    const first = { ...fixtureSource.initial, id: 'FIRST', valve: 'Первый' };
    const second = { ...fixtureSource.initial, id: 'SECOND', valve: 'Второй' };
    const draw = (history: (typeof first)[]) => (
      <FocusShell>
        <FixtureTests
          pageSize={20}
          onPageSize={() => {}}
          empty={false}
          showExample={false}
          draftAvailable={false}
          activeParameters={null}
          history={history}
          example={fixtureSource.initial}
          onNew={() => {}}
          onHistory={() => {}}
          onCurrent={() => {}}
        />
      </FocusShell>
    );
    const view = render(draw([first, second]));
    screen.getByRole('button', { name: /Открыть Первый/ }).focus();
    view.rerender(draw([second]));
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('main')));
  } finally {
    hasFocus.mockRestore();
  }
});

it('recovers focus when a retained view is hidden', async () => {
  const hasFocus = vi.spyOn(document, 'hasFocus').mockReturnValue(true);
  try {
    const view = render(<Retained hidden={false} />);
    screen.getByRole('button', { name: 'Действие' }).focus();
    view.rerender(<Retained hidden />);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('main')));
  } finally {
    hasFocus.mockRestore();
  }
});

it.each(['list', 'result'] as const)(
  'recovers focus when the real history %s is hidden',
  async (viewName) => {
    const hasFocus = vi.spyOn(document, 'hasFocus').mockReturnValue(true);
    try {
      const service = {
        ...desktopDemoService,
        listTests: vi.fn().mockResolvedValue({ items: [record], nextAfter: null }),
        test: vi.fn().mockResolvedValue(record),
      };
      const draw = (visible: boolean) => (
        <FocusShell>
          <History
            {...callbacks}
            service={service}
            available
            disabled={false}
            draftAvailable={false}
            visible={visible}
          />
        </FocusShell>
      );
      const view = render(draw(true));
      if (viewName === 'result') {
        await userEvent
          .setup()
          .click(await screen.findByRole('button', { name: /Открыть Первая/ }));
        (await screen.findByRole('button', { name: 'Новое по этим параметрам' })).focus();
      } else {
        screen.getByLabelText('Поиск по номеру или изделию').focus();
      }
      view.rerender(draw(false));
      await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('main')));
    } finally {
      hasFocus.mockRestore();
    }
  },
);

it('keeps keyboard focus in history when a refreshed page removes the focused row', async () => {
  const hasFocus = vi.spyOn(document, 'hasFocus').mockReturnValue(true);
  try {
    const service = {
      ...desktopDemoService,
      listTests: vi
        .fn()
        .mockResolvedValueOnce({ items: [record], nextAfter: null })
        .mockReturnValue(new Promise(() => {})),
    };
    const draw = (activeTestId?: string) => (
      <FocusShell>
        <History
          {...callbacks}
          service={service}
          available
          disabled={false}
          draftAvailable={false}
          activeTestId={activeTestId}
        />
      </FocusShell>
    );
    const view = render(draw());
    (await screen.findByRole('button', { name: /Открыть Первая/ })).focus();
    view.rerender(draw('77777777-7777-4777-8777-777777777777'));
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('main')));
  } finally {
    hasFocus.mockRestore();
  }
});

it('searches the complete valid Unicode product name', async () => {
  const service = {
    ...desktopDemoService,
    listTests: vi.fn().mockResolvedValue({ items: [], nextAfter: null }),
  };
  render(
    <History {...callbacks} service={service} available disabled={false} draftAvailable={false} />,
  );
  const user = userEvent.setup();
  const name = '𐐀'.repeat(150);
  await user.click(screen.getByLabelText('Поиск по номеру или изделию'));
  await user.paste(name);
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  await waitFor(() =>
    expect(service.listTests).toHaveBeenLastCalledWith(expect.objectContaining({ q: name })),
  );
});

it('preserves the query and rejects a search over the limit without truncating', async () => {
  const service = {
    ...desktopDemoService,
    listTests: vi.fn().mockResolvedValue({ items: [], nextAfter: null }),
  };
  render(
    <History {...callbacks} service={service} available disabled={false} draftAvailable={false} />,
  );
  const user = userEvent.setup();
  const field = screen.getByLabelText('Поиск по номеру или изделию');
  const name = 'А'.repeat(100) + 'Б'.repeat(100);
  await waitFor(() => expect(service.listTests).toHaveBeenCalledOnce());
  await user.click(field);
  await user.paste(name);
  await user.keyboard('{Home}X');
  expect(field).toHaveProperty('value', `X${name}`);
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  expect(screen.getByRole('alert').textContent).toMatch(/200/);
  expect(service.listTests).toHaveBeenCalledOnce();
  await user.clear(field);
  await user.paste(` ${name} `);
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  await waitFor(() =>
    expect(service.listTests).toHaveBeenLastCalledWith(expect.objectContaining({ q: name })),
  );
});

it.each(['ς', '[a]'])(
  'uses literal Unicode case folding in fixture history for %s',
  async (query) => {
    const entry = { ...fixtureSource.initial, id: 'history', valve: 'Σ клапан [A]' };
    const active = { ...entry, id: 'active' };
    const other = { ...entry, id: 'other', valve: 'Alpha' };
    render(
      <FixtureTests
        pageSize={20}
        onPageSize={() => {}}
        empty={false}
        showExample={false}
        draftAvailable={false}
        activeParameters={active}
        history={[entry, other]}
        example={fixtureSource.initial}
        onNew={() => {}}
        onHistory={() => {}}
        onCurrent={() => {}}
      />,
    );
    const user = userEvent.setup();
    await user.click(screen.getByLabelText('Поиск по номеру или изделию'));
    await user.paste(query);
    await user.click(screen.getByRole('button', { name: 'Найти' }));
    expect(screen.getByRole('button', { name: /Открыть Σ клапан \[A\]/ })).toBeTruthy();
    expect(screen.getByRole('button', { name: /Вернуться:.*Σ клапан \[A\]/ })).toBeTruthy();
    expect(screen.queryByRole('button', { name: /Открыть Alpha/ })).toBeNull();
  },
);

it('applies fixture history filters only after a valid search', async () => {
  render(
    <FixtureTests
      pageSize={20}
      onPageSize={() => {}}
      empty={false}
      showExample
      draftAvailable={false}
      activeParameters={null}
      history={[]}
      example={fixtureSource.initial}
      onNew={() => {}}
      onHistory={() => {}}
      onCurrent={() => {}}
    />,
  );
  const user = userEvent.setup();
  const field = screen.getByLabelText('Поиск по номеру или изделию');
  const example = fixtureSource.initial.valve;
  expect(screen.getByText(example)).toBeTruthy();
  await user.click(field);
  await user.paste('Я'.repeat(201));
  expect(screen.getByRole('alert')).toBeTruthy();
  expect(screen.getByText(example)).toBeTruthy();
  await user.clear(field);
  await user.type(field, 'нет такого изделия');
  expect(screen.getByText(example)).toBeTruthy();
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  expect(screen.queryByText(example)).toBeNull();
  await user.click(screen.getByRole('button', { name: 'Сбросить поиск' }));
  expect(screen.getByText(example)).toBeTruthy();
  await user.type(screen.getByLabelText('С даты'), '2026-11-06');
  await user.type(screen.getByLabelText('По дату'), '2026-11-05');
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  expect(screen.getByRole('alert').textContent).toMatch(/Начало диапазона/);
  expect(screen.queryByText(example)).toBeNull();
});

it('does not publish a page requested before the active identity changed while offline', async () => {
  let finish: (value: TestList) => void = () => {};
  const old = new Promise<TestList>((resolve) => {
    finish = resolve;
  });
  const service = {
    ...desktopDemoService,
    listTests: vi
      .fn()
      .mockReturnValueOnce(old)
      .mockResolvedValue({ items: [record], nextAfter: null }),
  };
  const view = render(
    <History {...callbacks} service={service} available disabled={false} draftAvailable={false} />,
  );
  await waitFor(() => expect(service.listTests).toHaveBeenCalledOnce());
  view.rerender(
    <History
      {...callbacks}
      service={service}
      available={false}
      disabled
      draftAvailable={false}
      activeTestId={record.testId}
    />,
  );
  await act(async () => {
    finish({ items: [], nextAfter: null });
    await old;
  });
  expect(screen.queryByText('История пуста')).toBeNull();
  view.rerender(
    <History
      {...callbacks}
      service={service}
      available
      disabled
      draftAvailable={false}
      activeTestId={record.testId}
      onCurrent={() => {}}
    />,
  );
  expect(
    await screen.findByRole('button', {
      name: /Вернуться: проход по последнему снимку.*Первая запись/,
    }),
  ).toBeTruthy();
});

it('opens the current pass when the requested record becomes active before its reply', async () => {
  let finish: (value: TestRecord) => void = () => {};
  const delayed = new Promise<TestRecord>((resolve) => {
    finish = resolve;
  });
  const service = {
    ...desktopDemoService,
    listTests: vi.fn().mockResolvedValue({ items: [record], nextAfter: null }),
    test: vi.fn().mockReturnValue(delayed),
  };
  const onCurrent = vi.fn();
  const view = render(
    <History
      {...callbacks}
      service={service}
      available
      disabled={false}
      draftAvailable={false}
      onCurrent={onCurrent}
    />,
  );
  await userEvent.setup().click(await screen.findByRole('button', { name: /Открыть Первая/ }));
  view.rerender(
    <History
      {...callbacks}
      service={service}
      available
      disabled
      draftAvailable={false}
      activeTestId={record.testId}
      onCurrent={onCurrent}
    />,
  );
  await act(async () => {
    finish(record);
    await delayed;
  });
  expect(onCurrent).toHaveBeenCalledOnce();
  expect(screen.queryByRole('heading', { name: 'Результат испытания' })).toBeNull();
});

it('returns to the active pass when the record read fails after the active identity appears', async () => {
  let reject: (reason: Error) => void = () => {};
  const delayed = new Promise<TestRecord>((_, fail) => {
    reject = fail;
  });
  const service = {
    ...desktopDemoService,
    listTests: vi.fn().mockResolvedValue({ items: [record], nextAfter: null }),
    test: vi.fn().mockReturnValue(delayed),
  };
  const onCurrent = vi.fn();
  const view = render(
    <History
      {...callbacks}
      service={service}
      available
      disabled={false}
      draftAvailable={false}
      onCurrent={onCurrent}
    />,
  );
  await userEvent.setup().click(await screen.findByRole('button', { name: /Открыть Первая/ }));
  view.rerender(
    <History
      {...callbacks}
      service={service}
      available={false}
      disabled
      draftAvailable={false}
      activeTestId={record.testId}
      onCurrent={onCurrent}
    />,
  );
  await act(async () => {
    reject(new Error('offline'));
    await delayed.catch(() => {});
  });
  expect(onCurrent).toHaveBeenCalledOnce();
  expect(screen.queryByText('Не удалось открыть запись из ядра.')).toBeNull();
});

it('returns to the active pass if the identity arrives after the record was opened', async () => {
  const service = {
    ...desktopDemoService,
    listTests: vi.fn().mockResolvedValue({ items: [record], nextAfter: null }),
    test: vi.fn().mockResolvedValue(record),
  };
  const onCurrent = vi.fn();
  const view = render(
    <History
      {...callbacks}
      service={service}
      available
      disabled={false}
      draftAvailable={false}
      onCurrent={onCurrent}
    />,
  );
  await userEvent.setup().click(await screen.findByRole('button', { name: /Открыть Первая/ }));
  await screen.findByRole('heading', { name: 'Результат испытания' });
  view.rerender(
    <History
      {...callbacks}
      service={service}
      available
      disabled
      draftAvailable={false}
      activeTestId={record.testId}
      onCurrent={onCurrent}
    />,
  );
  expect(onCurrent).toHaveBeenCalledOnce();
  expect(screen.queryByRole('heading', { name: 'Результат испытания' })).toBeNull();
});

it('honours an explicit return to the list when a hidden selected record became active', async () => {
  const service = {
    ...desktopDemoService,
    listTests: vi.fn().mockResolvedValue({ items: [record], nextAfter: null }),
    test: vi.fn().mockResolvedValue(record),
  };
  const onCurrent = vi.fn();
  const view = render(
    <History
      {...callbacks}
      service={service}
      available
      disabled={false}
      draftAvailable={false}
      onCurrent={onCurrent}
    />,
  );
  await userEvent.setup().click(await screen.findByRole('button', { name: /Открыть Первая/ }));
  await screen.findByRole('heading', { name: 'Результат испытания' });
  view.rerender(
    <History
      {...callbacks}
      service={service}
      available
      disabled
      draftAvailable={false}
      visible={false}
      activeTestId={record.testId}
      onCurrent={onCurrent}
    />,
  );
  view.rerender(
    <History
      {...callbacks}
      service={service}
      available
      disabled
      draftAvailable={false}
      activeTestId={record.testId}
      resetVersion={1}
      onCurrent={onCurrent}
    />,
  );
  expect(onCurrent).not.toHaveBeenCalled();
  expect(screen.queryByRole('heading', { name: 'Результат испытания' })).toBeNull();
});

it('refreshes the visible history when a newly created active record appears', async () => {
  const active = {
    ...record,
    stage: TestRecordStage.Preparing,
    outcome: TestRecordOutcome.TestRecordOutcomePending,
  };
  const service = {
    ...desktopDemoService,
    listTests: vi
      .fn()
      .mockResolvedValueOnce({ items: [], nextAfter: null })
      .mockResolvedValue({ items: [active], nextAfter: null }),
  };
  const view = render(
    <History {...callbacks} service={service} available disabled={false} draftAvailable={false} />,
  );
  await screen.findByText('История пуста');
  view.rerender(
    <History
      {...callbacks}
      service={service}
      available
      disabled
      draftAvailable={false}
      activeTestId={active.testId}
      onCurrent={() => {}}
    />,
  );
  expect(
    await screen.findByRole('button', {
      name: /Вернуться: проход по последнему снимку.*Первая запись/,
    }),
  ).toBeTruthy();
  expect(screen.queryByText('История пуста')).toBeNull();
});

it('keeps a matching active record in search results and returns to the current pass', async () => {
  const active = {
    ...record,
    stage: TestRecordStage.Running,
    outcome: TestRecordOutcome.TestRecordOutcomePending,
  };
  const onCurrent = vi.fn();
  const service = {
    ...desktopDemoService,
    listTests: vi.fn().mockResolvedValue({ items: [active], nextAfter: null }),
    test: vi.fn(),
  };
  render(
    <History
      {...callbacks}
      activeTestId={active.testId}
      onCurrent={onCurrent}
      service={service}
      available
      disabled
      draftAvailable={false}
    />,
  );
  const user = userEvent.setup();
  await user.type(screen.getByLabelText('Поиск по номеру или изделию'), active.testId);
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  expect(await screen.findByText('Ожидается', { selector: 'td' })).toBeTruthy();
  expect(screen.getByText('Проход по последнему снимку')).toBeTruthy();
  await user.click(
    await screen.findByRole('button', {
      name: /Вернуться: проход по последнему снимку.*Первая запись/,
    }),
  );
  expect(onCurrent).toHaveBeenCalledOnce();
  expect(service.test).not.toHaveBeenCalled();
  expect(screen.queryByText('По заданным условиям записей нет')).toBeNull();
  await user.selectOptions(screen.getByLabelText('Исход'), 'pending');
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  await waitFor(() =>
    expect(service.listTests).toHaveBeenLastCalledWith({
      q: active.testId,
      outcome: TestRecordOutcome.TestRecordOutcomePending,
      after: null,
      limit: 20,
    }),
  );
});

it('refreshes a late old saved record by its complete terminal version, not saveStatus alone', async () => {
  const old = {
    ...record,
    stage: TestRecordStage.Running,
    outcome: TestRecordOutcome.TestRecordOutcomePending,
    dataQuality: TestRecordDataQuality.TestRecordDataQualityPending,
  };
  let finish: (value: TestRecord) => void = () => {};
  const delayed = new Promise<TestRecord>((resolve) => {
    finish = resolve;
  });
  const service = {
    ...desktopDemoService,
    listTests: vi.fn().mockResolvedValue({ items: [old], nextAfter: null }),
    test: vi.fn().mockReturnValueOnce(delayed).mockResolvedValue(record),
  };
  const draw = (completedRecord: TestRecord) => (
    <History
      {...callbacks}
      completedRecord={completedRecord}
      service={service}
      available
      disabled={false}
      draftAvailable={false}
    />
  );
  const view = render(
    draw({ ...record, saveStatus: TestRecordSaveStatus.TestRecordSaveStatusPending }),
  );
  await userEvent.setup().click(await screen.findByRole('button', { name: /Открыть Первая/ }));
  view.rerender(draw(record));
  await act(async () => {
    finish(old);
    await delayed;
  });
  expect(await screen.findByText('Завершено')).toBeTruthy();
  expect(screen.getByText('Полные')).toBeTruthy();
  expect(service.test).toHaveBeenCalledTimes(2);
});

it.each(['success', 'error'])(
  'discards a late selected refresh %s after an explicit return to the list',
  async (outcome) => {
    const old = { ...record, saveStatus: TestRecordSaveStatus.TestRecordSaveStatusPending };
    let finish: (value: TestRecord) => void = () => {};
    let fail: (reason: Error) => void = () => {};
    const delayed = new Promise<TestRecord>((resolve, reject) => {
      finish = resolve;
      fail = reject;
    });
    const service = {
      ...desktopDemoService,
      listTests: vi.fn().mockResolvedValue({ items: [old], nextAfter: null }),
      test: vi.fn().mockResolvedValueOnce(old).mockReturnValueOnce(delayed),
    };
    const draw = (completedRecord: TestRecord) => (
      <History
        {...callbacks}
        completedRecord={completedRecord}
        service={service}
        available
        disabled={false}
        draftAvailable={false}
      />
    );
    const view = render(draw(old));
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /Открыть Первая/ }));
    await screen.findByRole('heading', { name: 'Результат испытания' });
    view.rerender(draw(record));
    await waitFor(() => expect(service.test).toHaveBeenCalledTimes(2));
    await user.click(screen.getByRole('button', { name: 'К списку' }));
    await act(async () => {
      if (outcome === 'success') finish(record);
      else fail(new Error('late'));
      await delayed.catch(() => {});
    });
    expect(screen.getByRole('heading', { name: 'Испытания' })).toBeTruthy();
    expect(screen.queryByRole('heading', { name: 'Результат испытания' })).toBeNull();
    expect(screen.queryByText('Не удалось обновить запись после сохранения.')).toBeNull();
  },
);

it('keeps list and selected-record read failures independent and retries each original operation', async () => {
  const old = { ...record, saveStatus: TestRecordSaveStatus.TestRecordSaveStatusPending };
  let failList: (reason: Error) => void = () => {};
  let failRecord: (reason: Error) => void = () => {};
  const listFailure = new Promise<TestList>((_, reject) => {
    failList = reject;
  });
  const recordFailure = new Promise<TestRecord>((_, reject) => {
    failRecord = reject;
  });
  const service = {
    ...desktopDemoService,
    listTests: vi
      .fn()
      .mockResolvedValueOnce({ items: [old], nextAfter: null })
      .mockReturnValueOnce(listFailure)
      .mockResolvedValue({ items: [record], nextAfter: null }),
    test: vi
      .fn()
      .mockResolvedValueOnce(old)
      .mockReturnValueOnce(recordFailure)
      .mockResolvedValue(record),
  };
  const draw = (completedRecord: TestRecord) => (
    <History
      {...callbacks}
      completedRecord={completedRecord}
      service={service}
      available
      disabled={false}
      draftAvailable={false}
    />
  );
  const view = render(draw(old));
  const user = userEvent.setup();
  await user.click(await screen.findByRole('button', { name: /Открыть Первая/ }));
  await screen.findByRole('heading', { name: 'Результат испытания' });
  view.rerender(draw(record));
  await waitFor(() => expect(service.test).toHaveBeenCalledTimes(2));
  await act(async () => {
    failRecord(new Error('record'));
    await recordFailure.catch(() => {});
  });
  await act(async () => {
    failList(new Error('list'));
    await listFailure.catch(() => {});
  });
  expect(screen.getByText('Не удалось обновить запись после сохранения.')).toBeTruthy();
  await user.click(screen.getByRole('button', { name: 'Повторить открытие записи' }));
  await screen.findByText('Сохранено');
  expect(service.listTests).toHaveBeenCalledTimes(2);
  await user.click(screen.getByRole('button', { name: 'К списку' }));
  expect(screen.getByText('Не удалось прочитать список испытаний из ядра.')).toBeTruthy();
  await user.click(screen.getByRole('button', { name: 'Повторить чтение списка' }));
  await screen.findByRole('button', { name: /Открыть Первая/ });
  expect(service.test).toHaveBeenCalledTimes(3);
});

it('ends the canceled loading state when a new query is submitted offline', async () => {
  let finish: (value: TestList) => void = () => {};
  const delayed = new Promise<TestList>((resolve) => {
    finish = resolve;
  });
  const service = {
    ...desktopDemoService,
    listTests: vi
      .fn()
      .mockReturnValueOnce(delayed)
      .mockResolvedValue({ items: [], nextAfter: null }),
  };
  const draw = (available: boolean) => (
    <History
      {...callbacks}
      service={service}
      available={available}
      disabled={false}
      draftAvailable={false}
    />
  );
  const view = render(draw(true));
  await waitFor(() => expect(service.listTests).toHaveBeenCalledOnce());
  view.rerender(draw(false));
  const user = userEvent.setup();
  await user.type(screen.getByLabelText('Поиск по номеру или изделию'), 'Новая выборка');
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  expect(screen.queryByText('Чтение истории…')).toBeNull();
  await act(async () => {
    finish({ items: [record], nextAfter: null });
    await delayed;
  });
  expect(screen.queryByRole('button', { name: /Открыть Первая/ })).toBeNull();
  view.rerender(draw(true));
  await waitFor(() =>
    expect(service.listTests).toHaveBeenLastCalledWith({
      q: 'Новая выборка',
      after: null,
      limit: 20,
    }),
  );
});

it('возвращает список измерений к началу только после успешного ответа', async () => {
  let finish: (page: MeasurementPage) => void = () => {};
  const delayed = new Promise<MeasurementPage>((resolve) => {
    finish = resolve;
  });
  const measurements = vi.fn()
    .mockResolvedValueOnce(measurementPage(100))
    .mockResolvedValueOnce(measurementPage(200))
    .mockReturnValueOnce(delayed);
  render(<RecordDetails record={record} service={{ ...desktopDemoService, measurements }} available />);
  const user = userEvent.setup();
  await user.click(screen.getByText('Измерения', { selector: 'summary' }));
  await user.click(await screen.findByRole('button', { name: 'Следующая страница измерений' }));
  await waitFor(() => expect(measurements).toHaveBeenCalledTimes(2));
  const table = document.querySelector<HTMLElement>('.table-scroll');
  if (!table) throw new Error('Measurement list missing');
  table.scrollTop = 120;
  let scrollPosition = 120;
  let rowsAtScroll = '';
  Object.defineProperty(table, 'scrollTop', {
    configurable: true,
    get: () => scrollPosition,
    set: (value: number) => {
      scrollPosition = value;
      rowsAtScroll = table.textContent ?? '';
    },
  });
  const first = screen.getByRole('button', { name: 'К началу измерений' });
  first.focus();
  await user.click(first);
  expect(table.scrollTop).toBe(120);
  expect(document.activeElement).toBe(first);
  await act(async () => { finish(measurementPage(100)); await delayed; });
  await waitFor(() => expect(table.scrollTop).toBe(0));
  expect(rowsAtScroll).toContain('100');
  expect(rowsAtScroll).not.toContain('200');
  expect(document.activeElement).toBe(first);
});

it('не сбрасывает страницу и прокрутку при обновлении сохранения', async () => {
  let finish: (page: MeasurementPage) => void = () => {};
  const delayed = new Promise<MeasurementPage>((resolve) => {
    finish = resolve;
  });
  const measurements = vi.fn()
    .mockResolvedValueOnce(measurementPage(100))
    .mockResolvedValueOnce(measurementPage(200))
    .mockReturnValueOnce(delayed);
  const pending = { ...record, saveStatus: TestRecordSaveStatus.TestRecordSaveStatusPending };
  const service = { ...desktopDemoService, measurements };
  const view = render(<RecordDetails record={pending} service={service} available />);
  const user = userEvent.setup();
  await user.click(screen.getByText('Измерения', { selector: 'summary' }));
  await user.click(await screen.findByRole('button', { name: 'Следующая страница измерений' }));
  await waitFor(() => expect(measurements).toHaveBeenCalledTimes(2));
  const table = document.querySelector<HTMLElement>('.table-scroll');
  if (!table) throw new Error('Measurement list missing');
  table.scrollTop = 120;
  view.rerender(<RecordDetails record={record} service={service} available />);
  await waitFor(() => expect(measurements).toHaveBeenLastCalledWith(record.testId, 100, 100));
  expect(document.querySelector('.table-scroll')).toBe(table);
  expect(table.scrollTop).toBe(120);
  await act(async () => { finish(measurementPage(200)); await delayed; });
  expect(table.scrollTop).toBe(120);
});

it('повторно загружает начальную страницу после обновления версии во время чтения', async () => {
  let finish: (page: MeasurementPage) => void = () => {};
  const delayed = new Promise<MeasurementPage>((resolve) => { finish = resolve; });
  const measurements = vi.fn().mockReturnValueOnce(delayed).mockResolvedValueOnce(measurementPage(null));
  const pending = { ...record, saveStatus: TestRecordSaveStatus.TestRecordSaveStatusPending };
  const service = { ...desktopDemoService, measurements };
  const view = render(<RecordDetails record={pending} service={service} available />);
  await userEvent.setup().click(screen.getByText('Измерения', { selector: 'summary' }));
  await waitFor(() => expect(measurements).toHaveBeenCalledTimes(1));
  view.rerender(<RecordDetails record={record} service={service} available />);
  await act(async () => { finish(measurementPage(null)); await delayed; });
  await waitFor(() => expect(measurements).toHaveBeenCalledTimes(2));
  expect(await screen.findByText(/201\s+Pa/)).toBeTruthy();
});

it('сохраняет неудавшийся возврат при подтверждении сохранения', async () => {
  const measurements = vi.fn()
    .mockResolvedValueOnce(measurementPage(100))
    .mockResolvedValueOnce(measurementPage(200))
    .mockRejectedValueOnce(new Error('read failed'))
    .mockResolvedValueOnce(measurementPage(100));
  const pending = { ...record, saveStatus: TestRecordSaveStatus.TestRecordSaveStatusPending };
  const service = { ...desktopDemoService, measurements };
  const view = render(<RecordDetails record={pending} service={service} available />);
  const user = userEvent.setup();
  await user.click(screen.getByText('Измерения', { selector: 'summary' }));
  await user.click(await screen.findByRole('button', { name: 'Следующая страница измерений' }));
  await waitFor(() => expect(measurements).toHaveBeenCalledTimes(2));
  const table = document.querySelector<HTMLElement>('.table-scroll');
  if (!table) throw new Error('Measurement list missing');
  table.scrollTop = 80;
  await user.click(screen.getByRole('button', { name: 'К началу измерений' }));
  await screen.findByText('Не удалось прочитать измерения.');
  expect(table.scrollTop).toBe(80);
  view.rerender(<RecordDetails record={record} service={service} available />);
  expect(measurements).toHaveBeenCalledTimes(3);
  await user.click(screen.getByRole('button', { name: 'Повторить чтение измерений' }));
  await waitFor(() => expect(measurements).toHaveBeenCalledTimes(4));
  expect(measurements).toHaveBeenLastCalledWith(record.testId, 0, 100);
  await waitFor(() => expect(table.scrollTop).toBe(0));
});

it('сохраняет фокус при повторном чтении после ошибки обновления', async () => {
  let finish: (page: MeasurementPage) => void = () => {};
  const delayed = new Promise<MeasurementPage>((resolve) => { finish = resolve; });
  const measurements = vi.fn()
    .mockResolvedValueOnce(measurementPage(null))
    .mockRejectedValueOnce(new Error('read failed'))
    .mockReturnValueOnce(delayed);
  const pending = { ...record, saveStatus: TestRecordSaveStatus.TestRecordSaveStatusPending };
  const service = { ...desktopDemoService, measurements };
  const view = render(<RecordDetails record={pending} service={service} available />);
  const user = userEvent.setup();
  await user.click(screen.getByText('Измерения', { selector: 'summary' }));
  await waitFor(() => expect(measurements).toHaveBeenCalledTimes(1));
  view.rerender(<RecordDetails record={record} service={service} available />);
  const retry = await screen.findByRole('button', { name: 'Повторить чтение измерений' });
  await user.click(retry);
  expect(document.activeElement).toBe(retry);
  await act(async () => { finish(measurementPage(null)); await delayed; });
  expect(document.activeElement).toBe(retry);
  await user.tab();
  expect(screen.queryByRole('button', { name: 'Повторить чтение измерений' })).toBeNull();
});

it('повторяет именно неудавшийся переход на следующую страницу', async () => {
  const measurements = vi.fn()
    .mockResolvedValueOnce(measurementPage(100))
    .mockRejectedValueOnce(new Error('read failed'))
    .mockResolvedValueOnce(measurementPage(null));
  render(<RecordDetails record={record} service={{ ...desktopDemoService, measurements }} available />);
  const user = userEvent.setup();
  await user.click(screen.getByText('Измерения', { selector: 'summary' }));
  await user.click(await screen.findByRole('button', { name: 'Следующая страница измерений' }));
  await user.click(await screen.findByRole('button', { name: 'Повторить чтение измерений' }));
  await waitFor(() => expect(measurements).toHaveBeenCalledTimes(3));
  expect(measurements).toHaveBeenLastCalledWith(record.testId, 100, 100);
  expect(screen.getByRole('button', { name: 'Повторить чтение измерений' }).getAttribute('aria-disabled')).toBe('true');
});

it('при отсутствии связи запоминает последнюю попытку чтения', async () => {
  const measurements = vi.fn()
    .mockResolvedValueOnce(measurementPage(100))
    .mockRejectedValueOnce(new Error('read failed'))
    .mockResolvedValueOnce(measurementPage(100));
  const service = { ...desktopDemoService, measurements };
  const view = render(<RecordDetails record={record} service={service} available />);
  const user = userEvent.setup();
  await user.click(screen.getByText('Измерения', { selector: 'summary' }));
  await user.click(await screen.findByRole('button', { name: 'Следующая страница измерений' }));
  await screen.findByText('Не удалось прочитать измерения.');
  view.rerender(<RecordDetails record={record} service={service} available={false} />);
  await user.click(screen.getByRole('button', { name: 'К началу измерений' }));
  expect(screen.getByText('Ожидание связи для чтения измерений.')).toBeTruthy();
  view.rerender(<RecordDetails record={record} service={service} available />);
  await user.click(screen.getByRole('button', { name: 'Повторить чтение измерений' }));
  await waitFor(() => expect(measurements).toHaveBeenCalledTimes(3));
  expect(measurements).toHaveBeenLastCalledWith(record.testId, 0, 100);
});

it('не заменяет офлайн-переход фоновым обновлением после восстановления связи', async () => {
  const measurements = vi.fn()
    .mockResolvedValueOnce(measurementPage(100))
    .mockResolvedValueOnce(measurementPage(200))
    .mockResolvedValueOnce(measurementPage(100));
  const pending = { ...record, saveStatus: TestRecordSaveStatus.TestRecordSaveStatusPending };
  const service = { ...desktopDemoService, measurements };
  const view = render(<RecordDetails record={pending} service={service} available />);
  const user = userEvent.setup();
  await user.click(screen.getByText('Измерения', { selector: 'summary' }));
  await user.click(await screen.findByRole('button', { name: 'Следующая страница измерений' }));
  await waitFor(() => expect(measurements).toHaveBeenCalledTimes(2));
  view.rerender(<RecordDetails record={record} service={service} available={false} />);
  await user.click(screen.getByRole('button', { name: 'К началу измерений' }));
  view.rerender(<RecordDetails record={record} service={service} available />);
  expect(measurements).toHaveBeenCalledTimes(2);
  await user.click(screen.getByRole('button', { name: 'Повторить чтение измерений' }));
  await waitFor(() => expect(measurements).toHaveBeenCalledTimes(3));
  expect(measurements).toHaveBeenLastCalledWith(record.testId, 0, 100);
});

it('сохраняет офлайн-переход при последующей смене версии записи', async () => {
  const measurements = vi.fn()
    .mockResolvedValueOnce(measurementPage(100))
    .mockResolvedValueOnce(measurementPage(200))
    .mockResolvedValueOnce(measurementPage(100));
  const pending = { ...record, saveStatus: TestRecordSaveStatus.TestRecordSaveStatusPending };
  const service = { ...desktopDemoService, measurements };
  const view = render(<RecordDetails record={pending} service={service} available />);
  const user = userEvent.setup();
  await user.click(screen.getByText('Измерения', { selector: 'summary' }));
  await user.click(await screen.findByRole('button', { name: 'Следующая страница измерений' }));
  await waitFor(() => expect(measurements).toHaveBeenCalledTimes(2));
  view.rerender(<RecordDetails record={pending} service={service} available={false} />);
  await user.click(screen.getByRole('button', { name: 'К началу измерений' }));
  view.rerender(<RecordDetails record={record} service={service} available />);
  expect(measurements).toHaveBeenCalledTimes(2);
  await user.click(screen.getByRole('button', { name: 'Повторить чтение измерений' }));
  await waitFor(() => expect(measurements).toHaveBeenCalledTimes(3));
  expect(measurements).toHaveBeenLastCalledWith(record.testId, 0, 100);
});

it('не применяет поздний возврат после закрытия измерений', async () => {
  let finish: (page: MeasurementPage) => void = () => {};
  const delayed = new Promise<MeasurementPage>((resolve) => { finish = resolve; });
  const measurements = vi.fn()
    .mockResolvedValueOnce(measurementPage(100))
    .mockReturnValueOnce(delayed)
    .mockResolvedValue(measurementPage(100));
  render(<RecordDetails record={record} service={{ ...desktopDemoService, measurements }} available />);
  const user = userEvent.setup();
  const summary = screen.getByText('Измерения', { selector: 'summary' });
  await user.click(summary);
  await screen.findByRole('button', { name: 'Следующая страница измерений' });
  await user.click(screen.getByRole('button', { name: 'К началу измерений' }));
  await user.click(summary);
  await user.click(summary);
  expect(measurements).toHaveBeenCalledTimes(2);
  await act(async () => { finish(measurementPage(null)); await delayed; });
  await waitFor(() => expect(measurements).toHaveBeenCalledTimes(3));
  expect(await screen.findByRole('button', { name: 'Следующая страница измерений' })).toBeTruthy();
});

it('не восстанавливает отменённый переход после закрытия и смены версии', async () => {
  let finish: (page: MeasurementPage) => void = () => {};
  const delayed = new Promise<MeasurementPage>((resolve) => { finish = resolve; });
  const measurements = vi.fn()
    .mockResolvedValueOnce(measurementPage(100))
    .mockReturnValueOnce(delayed)
    .mockResolvedValueOnce(measurementPage(100));
  const pending = { ...record, saveStatus: TestRecordSaveStatus.TestRecordSaveStatusPending };
  const service = { ...desktopDemoService, measurements };
  const view = render(<RecordDetails record={pending} service={service} available />);
  const user = userEvent.setup();
  const summary = screen.getByText('Измерения', { selector: 'summary' });
  await user.click(summary);
  await user.click(await screen.findByRole('button', { name: 'Следующая страница измерений' }));
  await user.click(summary);
  view.rerender(<RecordDetails record={record} service={service} available />);
  await act(async () => { finish(measurementPage(200)); await delayed; });
  await user.click(summary);
  await waitFor(() => expect(measurements).toHaveBeenCalledTimes(3));
  expect(measurements).toHaveBeenLastCalledWith(record.testId, 0, 100);
  expect(screen.queryByRole('button', { name: 'Повторить чтение измерений' })).toBeNull();
});

it('не предлагает отменённый повтор после потери связи и закрытия измерений', async () => {
  let finish: (page: MeasurementPage) => void = () => {};
  const delayed = new Promise<MeasurementPage>((resolve) => { finish = resolve; });
  const measurements = vi.fn()
    .mockResolvedValueOnce(measurementPage(100))
    .mockReturnValueOnce(delayed)
    .mockResolvedValueOnce(measurementPage(100));
  const service = { ...desktopDemoService, measurements };
  const view = render(<RecordDetails record={record} service={service} available />);
  const user = userEvent.setup();
  const summary = screen.getByText('Измерения', { selector: 'summary' });
  await user.click(summary);
  await user.click(await screen.findByRole('button', { name: 'Следующая страница измерений' }));
  view.rerender(<RecordDetails record={record} service={service} available={false} />);
  await user.click(summary);
  await act(async () => { finish(measurementPage(200)); await delayed; });
  view.rerender(<RecordDetails record={record} service={service} available />);
  await user.click(summary);
  await waitFor(() => expect(measurements).toHaveBeenCalledTimes(3));
  expect(measurements).toHaveBeenLastCalledWith(record.testId, 0, 100);
});

it('сохраняет фокус на переходе к последней странице измерений', async () => {
  const measurements = vi.fn()
    .mockResolvedValueOnce(measurementPage(100))
    .mockResolvedValueOnce(measurementPage(null));
  render(<RecordDetails record={record} service={{ ...desktopDemoService, measurements }} available />);
  const user = userEvent.setup();
  await user.click(screen.getByText('Измерения', { selector: 'summary' }));
  const next = await screen.findByRole('button', { name: 'Следующая страница измерений' });
  await user.click(next);
  await waitFor(() => expect(measurements).toHaveBeenCalledTimes(2));
  expect(screen.getByRole('button', { name: 'Следующая страница измерений' })).toBe(next);
  expect(next.getAttribute('aria-disabled')).toBe('true');
  expect(document.activeElement).toBe(next);
});

it('не применяет ответ измерений после потери актуальности сессии', async () => {
  let finish: (page: MeasurementPage) => void = () => {};
  const delayed = new Promise<MeasurementPage>((resolve) => { finish = resolve; });
  const measurements = vi.fn()
    .mockResolvedValueOnce(measurementPage(100))
    .mockReturnValueOnce(delayed)
    .mockResolvedValue(measurementPage(100));
  const service = { ...desktopDemoService, measurements };
  const view = render(<RecordDetails record={record} service={service} available />);
  const user = userEvent.setup();
  await user.click(screen.getByText('Измерения', { selector: 'summary' }));
  await screen.findByRole('button', { name: 'Следующая страница измерений' });
  const table = document.querySelector<HTMLElement>('.table-scroll');
  if (!table) throw new Error('Measurement list missing');
  table.scrollTop = 80;
  await user.click(screen.getByRole('button', { name: 'К началу измерений' }));
  view.rerender(<RecordDetails record={record} service={service} available={false} />);
  await act(async () => { finish(measurementPage(null)); await delayed; });
  expect(table.scrollTop).toBe(80);
  expect(screen.getByRole('button', { name: 'Следующая страница измерений' }).getAttribute('aria-disabled')).toBe('false');
  view.rerender(<RecordDetails record={record} service={service} available />);
  await user.click(screen.getByRole('button', { name: 'Повторить чтение измерений' }));
  await waitFor(() => expect(measurements).toHaveBeenCalledTimes(3));
});

it('показывает журнал событий понятным языком и сохраняет исходные сведения', async () => {
  const audit = vi.fn().mockResolvedValue({
    items: [{
      sequence: 1,
      testId: record.testId,
      revision: 2,
      occurredAt: '2026-11-05T12:34:56.123456Z',
      action: 'stop',
      outcome: 'accepted',
      operatorId: 'demo-operator',
      commandId: record.stopCommandId,
      actionId: '44444444-4444-4444-8444-444444444444',
      reason: 'measurement_write_timeout;future_reason',
      instruction: 'Откройте клапан',
    }, {
      sequence: 2,
      testId: record.testId,
      revision: 3,
      occurredAt: 'not-a-date',
      action: 'future_action',
      outcome: 'future_outcome',
      operatorId: 'future_actor',
      reason: null,
    }],
    nextAfter: null,
  });
  render(<RecordDetails record={record} service={{ ...desktopDemoService, audit }} available />);
  expect(audit).not.toHaveBeenCalled();
  const user = userEvent.setup();
  await user.click(screen.getByText('Журнал событий', { selector: 'summary' }));
  expect(await screen.findByText(/Остановка испытания.*Принято/)).toBeTruthy();
  expect(screen.getByText(/Срок ожидания записи измерений истёк/)).toBeTruthy();
  expect(screen.getByText(/Неизвестная причина/)).toBeTruthy();
  expect(screen.getByText('Неизвестное событие — Неизвестный результат')).toBeTruthy();
  expect(screen.getByText('Время неизвестно')).toBeTruthy();
  expect(screen.getAllByText(/Неизвестный участник/).length).toBeGreaterThan(0);
  expect(screen.getByText(new Intl.DateTimeFormat('ru-RU', { dateStyle: 'short', timeStyle: 'medium' }).format(new Date('2026-11-05T12:34:56.123456Z')))).toBeTruthy();
  expect(audit).toHaveBeenCalledTimes(1);
  const details = screen.getAllByText('Подробности события', { selector: 'summary' });
  if (!details[0]) throw new Error('Event details missing');
  await user.click(details[0]);
  expect(await screen.findByText('2026-11-05T12:34:56.123456Z')).toBeTruthy();
  expect(screen.getByText('measurement_write_timeout;future_reason')).toBeTruthy();
  expect(screen.getByText(record.stopCommandId)).toBeTruthy();
  expect(screen.getByText('44444444-4444-4444-8444-444444444444')).toBeTruthy();
  expect(screen.getByText('Откройте клапан')).toBeTruthy();
  expect(audit).toHaveBeenCalledTimes(1);
});

it('не объявляет принятую или неудачную остановку выполненной', () => {
  expect(eventActionLabel('stop')).toBe('Остановка испытания');
  expect(eventOutcomeLabel('accepted')).toBe('Принято');
  expect(eventOutcomeLabel('failed')).toBe('Ошибка выполнения');
  expect(eventOutcomeLabel('unknown')).toBe('Результат неизвестен');
  expect(eventReasonLabels('simulator_stop_failed;measurement_write_failed;future')).toEqual([
    'Остановка не подтверждена',
    'Ошибка записи измерений',
    'Неизвестная причина',
  ]);
});

it('сохраняет раскрытие события и фокус при обновлении журнала', async () => {
  let finish: (page: { items: typeof events; nextAfter: null }) => void = () => {};
  const events = [{
    sequence: 1,
    testId: record.testId,
    revision: 1,
    occurredAt: record.createdAt,
    action: 'start',
    outcome: 'accepted',
    operatorId: 'demo-operator',
  }];
  const delayed = new Promise<{ items: typeof events; nextAfter: null }>((resolve) => { finish = resolve; });
  const audit = vi.fn().mockResolvedValueOnce({ items: events, nextAfter: null }).mockReturnValueOnce(delayed);
  const service = { ...desktopDemoService, audit };
  const pending = { ...record, saveStatus: TestRecordSaveStatus.TestRecordSaveStatusPending };
  const view = render(<RecordDetails record={pending} service={service} available />);
  const user = userEvent.setup();
  await user.click(screen.getByText('Журнал событий', { selector: 'summary' }));
  const summary = await screen.findByText('Подробности события', { selector: 'summary' });
  await user.click(summary);
  summary.focus();
  await screen.findByText('Исходное время');
  view.rerender(<RecordDetails record={record} service={service} available />);
  expect(summary.isConnected).toBe(true);
  expect(document.activeElement).toBe(summary);
  expect(summary.closest('details')?.open).toBe(true);
  await act(async () => { finish({ items: events, nextAfter: null }); await delayed; });
  expect(document.activeElement).toBe(summary);
  expect(summary.closest('details')?.open).toBe(true);
});

it('сохраняет выбранную страницу журнала при обновлении версии', async () => {
  let finish: (page: { items: never[]; nextAfter: null }) => void = () => {};
  const delayed = new Promise<{ items: never[]; nextAfter: null }>((resolve) => { finish = resolve; });
  const first = {
    sequence: 100, testId: record.testId, revision: 1, occurredAt: record.createdAt,
    action: 'start', outcome: 'accepted', operatorId: 'demo-operator',
  };
  const audit = vi.fn()
    .mockResolvedValueOnce({ items: [first], nextAfter: 100 })
    .mockReturnValueOnce(delayed)
    .mockResolvedValueOnce({ items: [{ ...first, sequence: 101, action: 'complete' }], nextAfter: null });
  const service = { ...desktopDemoService, audit };
  const pending = { ...record, saveStatus: TestRecordSaveStatus.TestRecordSaveStatusPending };
  const view = render(<RecordDetails record={pending} service={service} available />);
  const user = userEvent.setup();
  await user.click(screen.getByText('Журнал событий', { selector: 'summary' }));
  await user.click(await screen.findByRole('button', { name: 'Следующая страница журнала' }));
  view.rerender(<RecordDetails record={record} service={service} available />);
  expect(audit).toHaveBeenCalledTimes(2);
  await act(async () => { finish({ items: [], nextAfter: null }); await delayed; });
  await waitFor(() => expect(audit).toHaveBeenCalledTimes(3));
  expect(audit).toHaveBeenLastCalledWith(record.testId, 100, 100);
  expect(await screen.findByText('Завершение испытания — Принято')).toBeTruthy();
});

it('не принимает поздний ответ журнала при потере связи и продолжает запрошенную страницу', async () => {
  const first = { sequence: 100, testId: record.testId, revision: 1, occurredAt: record.createdAt,
    action: 'start', outcome: 'accepted', operatorId: 'demo-operator' };
  let finish: (page: { items: typeof first[]; nextAfter: null }) => void = () => {};
  const delayed = new Promise<{ items: typeof first[]; nextAfter: null }>((resolve) => { finish = resolve; });
  const next = { ...first, sequence: 101, action: 'complete' };
  const audit = vi.fn()
    .mockResolvedValueOnce({ items: [first], nextAfter: 100 })
    .mockReturnValueOnce(delayed)
    .mockResolvedValueOnce({ items: [next], nextAfter: null });
  const service = { ...desktopDemoService, audit };
  const view = render(<RecordDetails record={record} service={service} available />);
  const user = userEvent.setup();
  await user.click(screen.getByText('Журнал событий', { selector: 'summary' }));
  await user.click(await screen.findByRole('button', { name: 'Следующая страница журнала' }));
  view.rerender(<RecordDetails record={record} service={service} available={false} />);
  await act(async () => { finish({ items: [next], nextAfter: null }); await delayed; });
  expect(screen.getByText('Запуск испытания — Принято')).toBeTruthy();
  expect(screen.queryByText('Завершение испытания — Принято')).toBeNull();
  view.rerender(<RecordDetails record={record} service={service} available />);
  await waitFor(() => expect(audit).toHaveBeenCalledTimes(3));
  expect(audit).toHaveBeenLastCalledWith(record.testId, 100, 100);
  expect(await screen.findByText('Завершение испытания — Принято')).toBeTruthy();
});

it('повторно читает устаревшую страницу журнала после ошибки и открытия', async () => {
  const audit = vi.fn()
    .mockResolvedValueOnce({ items: [], nextAfter: null })
    .mockRejectedValueOnce(new Error('read failed'))
    .mockResolvedValueOnce({ items: [], nextAfter: null });
  const service = { ...desktopDemoService, audit };
  const pending = { ...record, saveStatus: TestRecordSaveStatus.TestRecordSaveStatusPending };
  const view = render(<RecordDetails record={pending} service={service} available />);
  const user = userEvent.setup();
  const summary = screen.getByText('Журнал событий', { selector: 'summary' });
  await user.click(summary);
  await waitFor(() => expect(audit).toHaveBeenCalledTimes(1));
  view.rerender(<RecordDetails record={record} service={service} available />);
  await screen.findByText('Не удалось прочитать журнал.');
  await user.click(summary);
  await user.click(summary);
  await waitFor(() => expect(audit).toHaveBeenCalledTimes(3));
});

it('сохраняет неудавшийся переход журнала при обновлении и потере связи', async () => {
  const event = {
    sequence: 100, testId: record.testId, revision: 1, occurredAt: record.createdAt,
    action: 'start', outcome: 'accepted', operatorId: 'demo-operator',
  };
  const audit = vi.fn()
    .mockResolvedValueOnce({ items: [event], nextAfter: 100 })
    .mockRejectedValueOnce(new Error('read failed'))
    .mockResolvedValueOnce({ items: [{ ...event, sequence: 101, action: 'complete' }], nextAfter: null });
  const service = { ...desktopDemoService, audit };
  const pending = { ...record, saveStatus: TestRecordSaveStatus.TestRecordSaveStatusPending };
  const view = render(<RecordDetails record={pending} service={service} available />);
  const user = userEvent.setup();
  await user.click(screen.getByText('Журнал событий', { selector: 'summary' }));
  await user.click(await screen.findByRole('button', { name: 'Следующая страница журнала' }));
  await screen.findByText('Не удалось прочитать журнал.');
  view.rerender(<RecordDetails record={record} service={service} available={false} />);
  await user.click(screen.getByRole('button', { name: 'Следующая страница журнала' }));
  expect(screen.getByText('Ожидание связи для чтения журнала.')).toBeTruthy();
  view.rerender(<RecordDetails record={record} service={service} available />);
  await waitFor(() => expect(audit).toHaveBeenCalledTimes(3));
  expect(audit).toHaveBeenLastCalledWith(record.testId, 100, 100);
});

it('не перечитывает журнал при раскрытии подробностей события', async () => {
  const event = {
    sequence: 1, testId: record.testId, revision: 1, occurredAt: record.createdAt,
    action: 'start', outcome: 'accepted', operatorId: 'demo-operator',
  };
  const audit = vi.fn()
    .mockResolvedValueOnce({ items: [event], nextAfter: null })
    .mockRejectedValueOnce(new Error('read failed'));
  const service = { ...desktopDemoService, audit };
  const pending = { ...record, saveStatus: TestRecordSaveStatus.TestRecordSaveStatusPending };
  const view = render(<RecordDetails record={pending} service={service} available />);
  const user = userEvent.setup();
  await user.click(screen.getByText('Журнал событий', { selector: 'summary' }));
  const details = await screen.findByText('Подробности события', { selector: 'summary' });
  view.rerender(<RecordDetails record={record} service={service} available />);
  await screen.findByText('Не удалось прочитать журнал.');
  await user.click(details);
  await screen.findByText('Исходное время');
  view.rerender(<RecordDetails record={record} service={service} available={false} />);
  view.rerender(<RecordDetails record={record} service={service} available />);
  expect(audit).toHaveBeenCalledTimes(2);
});

it('сохраняет фокус при переходе на последнюю страницу журнала', async () => {
  const event = {
    sequence: 100, testId: record.testId, revision: 1, occurredAt: record.createdAt,
    action: 'start', outcome: 'accepted', operatorId: 'demo-operator',
  };
  const audit = vi.fn()
    .mockResolvedValueOnce({ items: [event], nextAfter: 100 })
    .mockResolvedValueOnce({ items: [{ ...event, sequence: 101, action: 'complete' }], nextAfter: null });
  render(<RecordDetails record={record} service={{ ...desktopDemoService, audit }} available />);
  const user = userEvent.setup();
  await user.click(screen.getByText('Журнал событий', { selector: 'summary' }));
  const next = await screen.findByRole('button', { name: 'Следующая страница журнала' });
  await user.click(next);
  await screen.findByText('Завершение испытания — Принято');
  expect(audit).toHaveBeenCalledTimes(2);
  expect(screen.getByRole('button', { name: 'Следующая страница журнала' })).toBe(next);
  expect(next.getAttribute('aria-disabled')).toBe('true');
  expect(document.activeElement).toBe(next);
});

it('refreshes open detail pages when final saving is confirmed while keeping disclosures open', async () => {
  const audit = vi
    .fn()
    .mockResolvedValueOnce({ items: [], nextAfter: null })
    .mockResolvedValue({
      items: [
        {
          sequence: 1,
          testId: record.testId,
          revision: 1,
          occurredAt: record.createdAt,
          action: 'complete',
          outcome: 'completed',
          operatorId: 'demo',
        },
      ],
      nextAfter: null,
    });
  const service = { ...desktopDemoService, audit };
  const view = render(
    <RecordDetails
      record={{ ...record, saveStatus: TestRecordSaveStatus.TestRecordSaveStatusPending }}
      service={service}
      available
    />,
  );
  await userEvent.setup().click(screen.getByText('Журнал событий', { selector: 'summary' }));
  await screen.findByText(/на этой странице нет сохранённых событий/i);
  view.rerender(<RecordDetails record={record} service={service} available />);
  expect(await screen.findByText('Завершение испытания — Выполнено')).toBeTruthy();
  expect(screen.queryByText(/на этой странице нет сохранённых событий/i)).toBeNull();
  expect(audit).toHaveBeenCalledTimes(2);
  expect(audit).toHaveBeenLastCalledWith(record.testId, 0, 100);
  await userEvent.setup().click(screen.getByText('Сведения о записи', { selector: 'summary' }));
  expect(
    screen.getByText('Сведения о записи', { selector: 'summary' }).closest('details')?.open,
  ).toBe(true);
  view.rerender(<RecordDetails record={record} service={service} available visible={false} />);
  expect(screen.queryByText('Завершение испытания — Выполнено')).toBeNull();
  view.rerender(<RecordDetails record={record} service={service} available />);
  expect(await screen.findByText('Завершение испытания — Выполнено')).toBeTruthy();
  expect(
    screen.getByText('Сведения о записи', { selector: 'summary' }).closest('details')?.open,
  ).toBe(true);
  expect(audit).toHaveBeenCalledTimes(2);
});

it('retries the failed page of the applied query without submitting edited filter fields', async () => {
  const listTests = vi
    .fn()
    .mockResolvedValueOnce({ items: [record], nextAfter: record.testId })
    .mockRejectedValueOnce(new Error('read failed'))
    .mockResolvedValueOnce({ items: [record], nextAfter: null });
  render(
    <History
      {...callbacks}
      service={{ ...desktopDemoService, listTests }}
      available
      disabled={false}
      draftAvailable={false}
    />,
  );
  const user = userEvent.setup();
  await user.click(await screen.findByRole('button', { name: 'Следующая страница' }));
  await user.type(screen.getByLabelText('Поиск по номеру или изделию'), 'Не отправлено');
  await user.click(screen.getByRole('button', { name: 'Повторить чтение списка' }));
  await screen.findByRole('button', { name: /Открыть Первая/ });
  expect(listTests).toHaveBeenLastCalledWith({ limit: 20, after: record.testId });
  expect(screen.getByLabelText('Поиск по номеру или изделию')).toHaveProperty(
    'value',
    'Не отправлено',
  );
});

it('keeps an already read page during snapshot renewal and refreshes it after confirmed saving', async () => {
  const pending = {
    ...record,
    saveStatus: TestRecordSaveStatus.TestRecordSaveStatusPending,
    outcome: TestRecordOutcome.TestRecordOutcomePending,
  };
  const listTests = vi
    .fn()
    .mockResolvedValueOnce({ items: [pending], nextAfter: null })
    .mockResolvedValue({ items: [record], nextAfter: null });
  const service = { ...desktopDemoService, listTests };
  const draw = (available: boolean, completedRecord: TestRecord) => (
    <History
      {...callbacks}
      completedRecord={completedRecord}
      service={service}
      available={available}
      disabled={false}
      draftAvailable={false}
    />
  );
  const view = render(draw(true, pending));
  await screen.findByRole('button', { name: /Открыть Первая/ });
  const calls = listTests.mock.calls.length;
  view.rerender(draw(false, pending));
  expect(screen.getByRole('button', { name: /Открыть Первая/ })).toBeTruthy();
  view.rerender(draw(true, pending));
  expect(listTests).toHaveBeenCalledTimes(calls);
  view.rerender(draw(true, record));
  expect(await screen.findByText('Сохранено')).toBeTruthy();
  expect(listTests).toHaveBeenCalledTimes(calls + 1);
});

it.each(['navigation', 'connection'])(
  'keeps an invalid applied range rejected across %s and recovers on reset',
  async (route) => {
    const listTests = vi.fn().mockResolvedValue({ items: [record], nextAfter: record.testId });
    const service = { ...desktopDemoService, listTests };
    const draw = (enabled: boolean) => (
      <History
        {...callbacks}
        service={service}
        available={route === 'connection' ? enabled : true}
        visible={route === 'navigation' ? enabled : true}
        disabled={false}
        draftAvailable={false}
      />
    );
    const view = render(draw(true));
    const user = userEvent.setup();
    await screen.findByRole('button', { name: /Открыть Первая/ });
    await user.type(screen.getByLabelText('С даты'), '2026-11-10');
    await user.type(screen.getByLabelText('По дату'), '2026-11-05');
    await user.click(screen.getByRole('button', { name: 'Найти' }));
    expect(screen.getByText('Начало диапазона должно быть не позже конца.')).toBeTruthy();
    const calls = listTests.mock.calls.length;
    await user.selectOptions(screen.getByLabelText('Записей на странице'), '50');
    expect(listTests).toHaveBeenCalledTimes(calls);
    view.rerender(draw(false));
    view.rerender(draw(true));
    expect(screen.getByText('Начало диапазона должно быть не позже конца.')).toBeTruthy();
    expect(listTests).toHaveBeenCalledTimes(calls);
    expect(screen.queryByRole('button', { name: /Открыть Первая/ })).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Сбросить поиск' }));
    expect(await screen.findByRole('button', { name: /Открыть Первая/ })).toBeTruthy();
    expect(listTests).toHaveBeenLastCalledWith({ limit: 50, after: null });
  },
);

it('returns from a copied draft to the list instead of resurrecting the saved result', async () => {
  const host = '11111111-1111-4111-8111-111111111111';
  const backend = '22222222-2222-4222-8222-222222222222';
  const session: SessionBridge = {
    beginSessionCheck: async () => 'ticket',
    keepAlive: async () => {},
    sessionView: async () => ({
      connection: {
        hostRunId: host,
        revision: 1,
        status: ConnectionStateStatus.Live,
        linkValid: true,
        leaseAcquireUnknown: false,
        streamGeneration: 0,
      },
      snapshot: {
        apiVersion: 'v1',
        backendRunId: backend,
        revision: 0,
        lastEventSequence: 0,
        measurementBatch: null,
        activeTest: null,
        pendingAction: null,
        measurementTiming: null,
        measurements: [],
      },
      validForMs: 6000,
      linkValidForMs: 6000,
    }),
  };
  const service = {
    ...desktopDemoService,
    listTests: vi.fn().mockResolvedValue({ items: [record], nextAfter: null }),
    test: vi.fn().mockResolvedValue(record),
  };
  render(
    <LiveApp
      session={session}
      service={service}
      hostRunId={host}
      backendRunId={backend}
      databaseId="77777777-7777-4777-8777-777777777777"
    />,
  );
  const user = userEvent.setup();
  await user.click(await screen.findByRole('button', { name: /Открыть Первая/ }));
  expect(await screen.findByText('Результат записи открыт')).toBeTruthy();
  await user.click(await screen.findByRole('button', { name: 'Новое по этим параметрам' }));
  expect(screen.queryByText('Результат записи открыт')).toBeNull();
  expect(screen.getByRole('main', { name: 'Параметры' })).toBeTruthy();
  await user.click(screen.getByRole('button', { name: 'К испытаниям' }));
  expect(screen.getByRole('main', { name: 'Испытания' })).toBe(document.activeElement);
  expect(await screen.findByRole('button', { name: /Открыть Первая/ })).toBeTruthy();
  expect(screen.queryByRole('heading', { name: 'Результат испытания' })).toBeNull();
  await user.click(screen.getByRole('button', { name: 'Продолжить подготовку' }));
  expect(screen.getByLabelText('Обозначение или наименование клапана')).toHaveProperty(
    'value',
    record.parameters.valve,
  );
  expect(service.test).toHaveBeenCalledOnce();
});

it('queries the whole source with calendar/outcome filters, resets the cursor and rejects a late old page', async () => {
  let finish: (value: TestList) => void = () => {};
  const oldPage = new Promise<TestList>((resolve) => {
    finish = resolve;
  });
  const listTests = vi
    .fn()
    .mockResolvedValueOnce({ items: [record], nextAfter: record.testId })
    .mockReturnValueOnce(oldPage)
    .mockResolvedValueOnce({
      items: [
        {
          ...record,
          testId: '66666666-6666-4666-8666-666666666666',
          parameters: { ...record.parameters, valve: 'За первой страницей' },
        },
      ],
      nextAfter: null,
    });
  render(
    <History
      {...callbacks}
      service={{ ...desktopDemoService, listTests }}
      available
      disabled={false}
      draftAvailable={false}
    />,
  );
  const user = userEvent.setup();
  await user.click(await screen.findByRole('button', { name: 'Следующая страница' }));
  expect(listTests).toHaveBeenLastCalledWith({ limit: 20, after: record.testId });
  await user.type(screen.getByLabelText('Поиск по номеру или изделию'), 'За первой');
  await user.selectOptions(screen.getByLabelText('Исход'), 'completed');
  await user.type(screen.getByLabelText('С даты'), '2026-11-05');
  await user.type(screen.getByLabelText('По дату'), '2026-11-05');
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  expect(await screen.findByRole('button', { name: /Открыть За первой страницей/ })).toBeTruthy();
  const start = new Date('2026-11-05T00:00:00');
  const end = new Date('2026-11-06T00:00:00');
  expect(listTests).toHaveBeenLastCalledWith({
    limit: 20,
    after: null,
    q: 'За первой',
    outcome: 'completed',
    createdFrom: start.toISOString(),
    createdBefore: end.toISOString(),
  });
  await act(async () => {
    finish({ items: [record], nextAfter: record.testId });
    await oldPage;
  });
  expect(screen.queryByRole('button', { name: /Открыть Первая запись/ })).toBeNull();
  expect(screen.queryByRole('button', { name: 'Следующая страница' })).toBeNull();
});

it('keeps filters and the current bounded page across hidden navigation and ignores a late record open', async () => {
  let finish: (value: TestRecord) => void = () => {};
  const pending = new Promise<TestRecord>((resolve) => {
    finish = resolve;
  });
  const listTests = vi.fn().mockResolvedValue({ items: [record], nextAfter: null });
  const service = { ...desktopDemoService, listTests, test: vi.fn().mockReturnValue(pending) };
  const onViewChange = vi.fn();
  const draw = (visible: boolean) => (
    <History
      {...callbacks}
      onViewChange={onViewChange}
      visible={visible}
      service={service}
      available
      disabled={false}
      draftAvailable={false}
    />
  );
  const view = render(draw(true));
  const user = userEvent.setup();
  await user.type(screen.getByLabelText('Поиск по номеру или изделию'), 'Первая');
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  await user.click(await screen.findByRole('button', { name: /Открыть Первая/ }));
  view.rerender(draw(false));
  await act(async () => {
    finish(record);
    await pending;
  });
  expect(onViewChange).not.toHaveBeenCalled();
  const calls = listTests.mock.calls.length;
  view.rerender(draw(true));
  await waitFor(() => expect(listTests).toHaveBeenCalledTimes(calls + 1));
  expect(screen.getByLabelText('Поиск по номеру или изделию')).toHaveProperty('value', 'Первая');
  expect(listTests).toHaveBeenLastCalledWith({ limit: 20, after: null, q: 'Первая' });
});

it('opens the shared result before reading details and retains the record when a detail page fails', async () => {
  const measurements = vi
    .fn()
    .mockRejectedValueOnce(new Error('offline'))
    .mockResolvedValue({ items: [], nextAfter: null });
  const audit = vi.fn().mockResolvedValue({ items: [], nextAfter: null });
  const onCopy = vi.fn();
  render(
    <History
      {...callbacks}
      onCopy={onCopy}
      service={{
        ...desktopDemoService,
        listTests: vi.fn().mockResolvedValue({ items: [record], nextAfter: null }),
        test: vi.fn().mockResolvedValue(record),
        measurements,
        audit,
      }}
      available
      disabled={false}
      draftAvailable={false}
    />,
  );
  const user = userEvent.setup();
  await user.click(await screen.findByRole('button', { name: /Открыть Первая/ }));
  expect(await screen.findByRole('heading', { name: 'Результат испытания' })).toBeTruthy();
  expect(screen.getByText('Ход испытания')).toBeTruthy();
  expect(screen.getByText('Данные')).toBeTruthy();
  expect(screen.getByText('Сохранение')).toBeTruthy();
  expect(measurements).not.toHaveBeenCalled();
  expect(audit).not.toHaveBeenCalled();
  await user.click(screen.getByText('Измерения', { selector: 'summary' }));
  expect(await screen.findByText('Не удалось прочитать измерения.')).toBeTruthy();
  expect(screen.getByRole('heading', { name: 'Результат испытания' })).toBeTruthy();
  await user.click(screen.getByRole('button', { name: 'К началу измерений' }));
  expect(await screen.findByText(/на этой странице нет сохранённых измерений/i)).toBeTruthy();
  expect(measurements).toHaveBeenLastCalledWith(record.testId, 0, 100);
  await user.click(screen.getByRole('button', { name: 'Новое по этим параметрам' }));
  expect(onCopy).toHaveBeenCalledWith(record.parameters);
});

it('uses operator language for saved measurement quality and source', async () => {
  const measurements = vi.fn().mockResolvedValue({
    items: [
      {
        sequence: 1,
        channel: 'dd1',
        value: 1000,
        unit: 'Pa',
        quality: 'valid',
        measuredAt: null,
        source: 'simulator',
      },
    ],
    nextAfter: null,
  });
  render(
    <RecordDetails record={record} service={{ ...desktopDemoService, measurements }} available />,
  );
  await userEvent.setup().click(screen.getByText('Измерения', { selector: 'summary' }));
  expect(await screen.findByText('достоверно')).toBeTruthy();
  expect(screen.getByText('симулятор')).toBeTruthy();
});

it('starts a changed offline query at the first cursor on reconnect, including unknown outcomes', async () => {
  const listTests = vi.fn().mockResolvedValue({ items: [record], nextAfter: record.testId });
  const service = { ...desktopDemoService, listTests };
  const draw = (available: boolean) => (
    <History
      {...callbacks}
      service={service}
      available={available}
      disabled={false}
      draftAvailable={false}
    />
  );
  const view = render(draw(true));
  const user = userEvent.setup();
  await user.click(await screen.findByRole('button', { name: 'Следующая страница' }));
  await screen.findByRole('button', { name: /Открыть Первая/ });
  view.rerender(draw(false));
  await user.selectOptions(screen.getByLabelText('Записей на странице'), '50');
  await user.type(screen.getByLabelText('Поиск по номеру или изделию'), 'Другая выборка');
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  view.rerender(draw(true));
  await waitFor(() =>
    expect(listTests).toHaveBeenLastCalledWith({ limit: 50, after: null, q: 'Другая выборка' }),
  );
  await user.selectOptions(screen.getByLabelText('Исход'), 'unknown');
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  await waitFor(() =>
    expect(listTests).toHaveBeenLastCalledWith({
      limit: 50,
      after: null,
      q: 'Другая выборка',
      outcome: 'unknown',
    }),
  );
});

it('cancels pending record opening when navigation explicitly returns to the list', async () => {
  let finish: (value: TestRecord) => void = () => {};
  const pending = new Promise<TestRecord>((resolve) => {
    finish = resolve;
  });
  const service = {
    ...desktopDemoService,
    listTests: vi.fn().mockResolvedValue({ items: [record], nextAfter: null }),
    test: vi.fn().mockReturnValue(pending),
  };
  const onViewChange = vi.fn();
  const draw = (resetVersion: number) => (
    <History
      {...callbacks}
      service={service}
      available
      disabled={false}
      draftAvailable={false}
      resetVersion={resetVersion}
      onViewChange={onViewChange}
    />
  );
  const view = render(draw(0));
  await userEvent.setup().click(await screen.findByRole('button', { name: /Открыть Первая/ }));
  view.rerender(draw(1));
  expect(await screen.findByRole('button', { name: /Открыть Первая/ })).toBeTruthy();
  await act(async () => {
    finish(record);
    await pending;
  });
  expect(onViewChange).not.toHaveBeenCalled();
});
