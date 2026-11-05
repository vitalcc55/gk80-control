import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';

vi.mock('../src/components/MeasurementChart', () => ({ MeasurementChart: () => null }));

import type { ReactNode } from 'react';
import { AppShell } from '../src/components/AppShell';
import type { SessionBridge } from '../src/desktop/bridge';
import { History } from '../src/desktop/History';
import { LiveApp } from '../src/desktop/LiveApp';
import { RecordDetails } from '../src/desktop/RecordDetails';
import { desktopDemoService } from '../src/desktop/wails-bridge';
import { FixtureTests } from '../src/FixtureTests';
import { fixtureSource } from '../src/fixtureAdapter';
import {
  ConnectionStateStatus,
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
  expect(await screen.findByText(/По последнему снимку: Прервано.*Ошибка сохранения/)).toBeTruthy();
  await userEvent.setup().click(screen.getByRole('button', { name: /Открыть Первая/ }));
  expect(await screen.findByText(/В истории сохранена другая версия/)).toBeTruthy();
  expect(screen.getByText(/По последнему снимку: Прервано.*Ошибка сохранения/)).toBeTruthy();
  expect(screen.getByText('Ожидается')).toBeTruthy();
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

it('applies fixture history filters only after a valid search', async () => {
  render(
    <FixtureTests
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
      limit: 100,
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
      limit: 100,
    }),
  );
});

it('refreshes open detail pages when final saving is confirmed while keeping disclosures open', async () => {
  const audit = vi
    .fn()
    .mockResolvedValueOnce({ items: [], nextAfter: null })
    .mockResolvedValue({
      items: [
        {
          sequence: 1,
          occurredAt: record.createdAt,
          action: 'finish',
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
  await userEvent.setup().click(screen.getByText('Аудит', { selector: 'summary' }));
  await screen.findByText('Событий нет.');
  view.rerender(<RecordDetails record={record} service={service} available />);
  expect(await screen.findByText(/finish · completed/)).toBeTruthy();
  expect(screen.queryByText('Событий нет.')).toBeNull();
  expect(audit).toHaveBeenCalledTimes(2);
  expect(audit).toHaveBeenLastCalledWith(record.testId, 0, 100);
  await userEvent.setup().click(screen.getByText('Сведения о записи', { selector: 'summary' }));
  expect(
    screen.getByText('Сведения о записи', { selector: 'summary' }).closest('details')?.open,
  ).toBe(true);
  view.rerender(<RecordDetails record={record} service={service} available visible={false} />);
  expect(screen.queryByText(/finish · completed/)).toBeNull();
  view.rerender(<RecordDetails record={record} service={service} available />);
  expect(await screen.findByText(/finish · completed/)).toBeTruthy();
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
  expect(listTests).toHaveBeenLastCalledWith({ limit: 100, after: record.testId });
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
    view.rerender(draw(false));
    view.rerender(draw(true));
    expect(screen.getByText('Начало диапазона должно быть не позже конца.')).toBeTruthy();
    expect(listTests).toHaveBeenCalledTimes(calls);
    expect(screen.queryByRole('button', { name: /Открыть Первая/ })).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Сбросить поиск' }));
    expect(await screen.findByRole('button', { name: /Открыть Первая/ })).toBeTruthy();
    expect(listTests).toHaveBeenLastCalledWith({ limit: 100, after: null });
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
  expect(listTests).toHaveBeenLastCalledWith({ limit: 100, after: record.testId });
  await user.type(screen.getByLabelText('Поиск по номеру или изделию'), 'За первой');
  await user.selectOptions(screen.getByLabelText('Исход'), 'completed');
  await user.type(screen.getByLabelText('С даты'), '2026-11-05');
  await user.type(screen.getByLabelText('По дату'), '2026-11-05');
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  expect(await screen.findByRole('button', { name: /Открыть За первой страницей/ })).toBeTruthy();
  const start = new Date('2026-11-05T00:00:00');
  const end = new Date('2026-11-06T00:00:00');
  expect(listTests).toHaveBeenLastCalledWith({
    limit: 100,
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
  expect(listTests).toHaveBeenLastCalledWith({ limit: 100, after: null, q: 'Первая' });
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
  expect(await screen.findByText('Измерений нет.')).toBeTruthy();
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
  await user.type(screen.getByLabelText('Поиск по номеру или изделию'), 'Другая выборка');
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  view.rerender(draw(true));
  await waitFor(() =>
    expect(listTests).toHaveBeenLastCalledWith({ limit: 100, after: null, q: 'Другая выборка' }),
  );
  await user.selectOptions(screen.getByLabelText('Исход'), 'unknown');
  await user.click(screen.getByRole('button', { name: 'Найти' }));
  await waitFor(() =>
    expect(listTests).toHaveBeenLastCalledWith({
      limit: 100,
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
