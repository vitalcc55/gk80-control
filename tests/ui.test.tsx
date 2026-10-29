import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { App } from '../src/App';

// uPlot requires a canvas; its real rendering is covered in the browser tests.
vi.mock('../src/components/MeasurementChart', () => ({
  MeasurementChart: () => <div role="img" aria-label="График подготовленных показаний" />,
}));

beforeEach(() => {
  window.history.replaceState(null, '', '/');
});

describe('подготовленные состояния оператора', () => {
  it('в примере с приводом не требует ручного подтверждения серии', async () => {
    const user = userEvent.setup();
    window.history.replaceState(null, '', '/?fixture=actuator');
    render(<App />);
    expect(screen.getByText('Цикл 4 из 10 · пример')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Подтверждаю завершение серии' })).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Показать следующий этап' }));
    expect(screen.getByRole('heading', { name: 'Выход на рабочее давление' })).toBeTruthy();
  });

  it('после запроса остановки на подготовительных циклах не придумывает показания', async () => {
    const user = userEvent.setup();
    window.history.replaceState(null, '', '/?fixture=actuator');
    render(<App />);
    await user.click(screen.getByRole('button', { name: 'Остановить испытание' }));
    const pressure = screen.getByText('Давление в ёмкости').closest('.reading');
    const differential = screen.getByText('Перепад на диафрагме').closest('.reading');
    if (!(pressure instanceof HTMLElement) || !(differential instanceof HTMLElement))
      throw new Error('Readings missing');
    expect(within(pressure).getByText('Неизвестно')).toBeTruthy();
    expect(within(differential).getByText('Неизвестно')).toBeTruthy();
    expect(new URLSearchParams(window.location.search).get('step')).toBe('0');
    expect(screen.queryByRole('button', { name: 'Показать результат' })).toBeNull();
    await user.click(screen.getByRole('button', { name: 'К испытаниям' }));
    expect(screen.getByRole('button', { name: 'Новое испытание' })).toHaveProperty(
      'disabled',
      true,
    );
    expect(screen.getByRole('button', { name: 'Вернуться к испытанию' })).toBeTruthy();
  });

  it('не меняет подготовленное показание на снижении давления при запросе остановки', async () => {
    const user = userEvent.setup();
    window.history.replaceState(null, '', '/?fixture=waiting&step=7');
    render(<App />);
    const pressure = screen.getByText('Давление в ёмкости').closest('.reading');
    if (!(pressure instanceof HTMLElement)) throw new Error('Pressure reading missing');
    expect(pressure.querySelector('strong')?.textContent).toMatch(/2\s*000\s*Па/);
    await user.click(screen.getByRole('button', { name: 'Остановить испытание' }));
    expect(pressure.querySelector('strong')?.textContent).toMatch(/2\s*000\s*Па/);
    expect(new URLSearchParams(window.location.search).get('step')).toBe('7');
  });

  it('при потере ДД1 на снижении давления не показывает текущее значение', () => {
    window.history.replaceState(null, '', '/?fixture=sensor-lost&step=7');
    render(<App />);
    const pressure = screen.getByText('Давление в ёмкости').closest('.reading');
    if (!(pressure instanceof HTMLElement)) throw new Error('Pressure reading missing');
    expect(within(pressure).getByText('Неизвестно')).toBeTruthy();
    expect(within(pressure).getByText(/устарело/)).toBeTruthy();
  });
  it('сохраняет незавершённый ввод при навигации и заново запрашивает подтверждения готовности', async () => {
    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByRole('button', { name: 'Новое испытание' }));
    const valve = screen.getByRole('textbox', { name: 'Обозначение или наименование клапана' });
    await user.type(valve, 'Клапан П-9');
    const area = screen.getByRole('textbox', { name: /Площадь клапана/ });
    await user.clear(area);
    await user.type(area, '0,');
    await user.click(
      within(screen.getByRole('navigation')).getByRole('button', { name: 'Испытания' }),
    );
    await user.click(screen.getByRole('button', { name: 'Продолжить подготовку' }));
    expect(screen.getByRole('textbox', { name: /Площадь клапана/ })).toHaveProperty('value', '0,');
    expect(
      screen.getByRole('textbox', { name: 'Обозначение или наименование клапана' }),
    ).toHaveProperty('value', 'Клапан П-9');
    await user.type(screen.getByRole('textbox', { name: /Площадь клапана/ }), '12');
    await user.click(screen.getByRole('button', { name: 'Проверить готовность' }));
    await user.click(screen.getByRole('checkbox', { name: /Клапан закрыт/ }));
    await user.click(
      within(screen.getByRole('navigation')).getByRole('button', { name: 'Параметры' }),
    );
    await user.click(screen.getByRole('button', { name: 'Проверить готовность' }));
    expect(screen.getByRole('checkbox', { name: /Клапан закрыт/ })).toHaveProperty(
      'checked',
      false,
    );
    expect(screen.getByRole('button', { name: 'Перейти к проведению' })).toHaveProperty(
      'disabled',
      true,
    );
  });

  it('показывает параметры активного прохода после истории и возвращает к прежнему этапу', async () => {
    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByRole('button', { name: 'Новое испытание' }));
    await user.type(
      screen.getByRole('textbox', { name: 'Обозначение или наименование клапана' }),
      'Клапан П-10',
    );
    await user.click(screen.getByRole('button', { name: 'Проверить готовность' }));
    await user.click(screen.getByRole('checkbox', { name: /Клапан закрыт/ }));
    await user.click(
      screen.getByRole('checkbox', { name: /Подключение соответствует направлению/ }),
    );
    await user.click(screen.getByRole('button', { name: 'Перейти к проведению' }));
    await user.click(screen.getByRole('button', { name: 'Подтверждаю завершение серии' }));
    const nav = screen.getByRole('navigation');
    expect(within(nav).queryByRole('status')).toBeNull();
    await user.click(within(nav).getByRole('button', { name: 'Испытания' }));
    expect(within(nav).queryByRole('status')).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Открыть' }));
    await user.click(within(nav).getByRole('button', { name: 'Параметры' }));
    expect(screen.getByText('Текущее испытание · только просмотр')).toBeTruthy();
    expect(screen.getByText('Клапан П-10')).toBeTruthy();
    expect(screen.queryByRole('textbox')).toBeNull();
    await user.click(within(nav).getByRole('button', { name: 'Текущее испытание' }));
    expect(screen.getByRole('heading', { name: 'Выход на рабочее давление' })).toBeTruthy();
    expect(screen.getByText(/Клапан П-10/)).toBeTruthy();
  });

  it('после просмотра параметров сохраняет запрос остановки и потерю ДД1', async () => {
    const user = userEvent.setup();
    window.history.replaceState(null, '', '/?fixture=sensor-lost');
    render(<App />);
    const nav = screen.getByRole('navigation');
    await user.click(within(nav).getByRole('button', { name: 'Параметры' }));
    expect(within(nav).getByText('Пример: потеря ДД1')).toBeTruthy();
    await user.click(within(nav).getByRole('button', { name: 'Текущее испытание' }));
    await user.click(screen.getByRole('button', { name: 'Остановить испытание' }));
    await user.click(within(nav).getByRole('button', { name: 'Параметры' }));
    expect(within(nav).getByText('Пример: потеря ДД1 · остановка не подтверждена')).toBeTruthy();
    await user.click(within(nav).getByRole('button', { name: 'Текущее испытание' }));
    expect(screen.getByText('Данные ДД1 недоступны')).toBeTruthy();
    expect(within(nav).queryByRole('status')).toBeNull();
    expect(screen.getByRole('button', { name: 'Остановка выполняется' })).toHaveProperty(
      'disabled',
      true,
    );
    expect(screen.getByText(/Последнее:.*устарело/)).toBeTruthy();
  });
  it('не пропускает пустое обязательное поле и сохраняет корректный ввод', async () => {
    const user = userEvent.setup();
    render(<App />);
    const newButton = screen.getAllByRole('button', { name: 'Новое испытание' })[0];
    if (!newButton) throw new Error('New test button missing');
    await user.click(newButton);
    const area = screen.getByRole('textbox', { name: /Площадь клапана/ });
    await user.clear(area);
    await user.type(area, '0,12');
    await user.click(screen.getByRole('button', { name: 'Проверить готовность' }));
    expect(screen.getByText('Укажите обозначение или наименование клапана.')).toBeTruthy();
    expect(area).toHaveProperty('value', '0,12');
    expect(screen.queryByRole('heading', { name: 'Готовность' })).toBeNull();
  });

  it('объясняет блокировку и разрешает переход только после двух подтверждений', async () => {
    const user = userEvent.setup();
    window.history.replaceState(null, '', '/?fixture=readiness');
    render(<App />);
    const next = screen.getByRole('button', { name: 'Перейти к проведению' });
    expect(next).toHaveProperty('disabled', true);
    expect(screen.getByText('Отметьте оба подтверждения оператора.')).toBeTruthy();
    await user.click(screen.getByRole('checkbox', { name: /Клапан закрыт/ }));
    expect(next).toHaveProperty('disabled', true);
    await user.click(
      screen.getByRole('checkbox', { name: /Подключение соответствует направлению/ }),
    );
    expect(next).toHaveProperty('disabled', false);
    await user.click(next);
    expect(screen.getByRole('heading', { name: 'Проведение испытания' })).toBeTruthy();
  });

  it('показывает потерю ДД1 как неизвестное значение с пометкой устаревания', () => {
    window.history.replaceState(null, '', '/?fixture=sensor-lost');
    render(<App />);
    const pressure = screen.getByText('Давление в ёмкости').closest('.reading');
    expect(pressure).not.toBeNull();
    if (!(pressure instanceof HTMLElement)) throw new Error('Pressure reading missing');
    expect(within(pressure).getByText('Неизвестно')).toBeTruthy();
    expect(within(pressure).getByText(/устарело/)).toBeTruthy();
    expect(within(pressure).queryByText(/^0\s*Па$/)).toBeNull();
  });

  it('отличает запрос и выполнение остановки от её подтверждения', async () => {
    const user = userEvent.setup();
    window.history.replaceState(null, '', '/?fixture=waiting');
    render(<App />);
    await user.click(screen.getByRole('button', { name: 'Остановить испытание' }));
    expect(
      screen.getByText(/Завершение остановки и безопасное давление не подтверждены/),
    ).toBeTruthy();
    const stopping = screen.getByRole('button', { name: 'Остановка выполняется' });
    expect(stopping).toHaveProperty('disabled', true);
    expect(screen.queryByText('Испытание остановлено')).toBeNull();
  });

  it('после потери ДД1 остановка не возвращает неизвестное давление к достоверному значению', async () => {
    const user = userEvent.setup();
    window.history.replaceState(null, '', '/?fixture=sensor-lost');
    render(<App />);
    await user.click(screen.getByRole('button', { name: 'Остановить испытание' }));
    const pressure = screen.getByText('Давление в ёмкости').closest('.reading');
    if (!(pressure instanceof HTMLElement)) throw new Error('Pressure reading missing');
    expect(within(pressure).getByText('Неизвестно')).toBeTruthy();
    expect(within(pressure).queryByText(/^0\s*Па$/)).toBeNull();
    expect(screen.getByRole('button', { name: 'Остановка выполняется' })).toHaveProperty(
      'disabled',
      true,
    );
  });

  it('возвращается к текущему этапу с введёнными параметрами после просмотра списка', async () => {
    const user = userEvent.setup();
    render(<App />);
    const newButton = screen.getAllByRole('button', { name: 'Новое испытание' })[0];
    if (!newButton) throw new Error('New test button missing');
    await user.click(newButton);
    await user.type(
      screen.getByRole('textbox', { name: 'Обозначение или наименование клапана' }),
      'Клапан П-7',
    );
    const pressure = screen.getByRole('textbox', { name: 'Pmax производителя, Па' });
    await user.clear(pressure);
    await user.type(pressure, '345,67');
    await user.click(screen.getByRole('button', { name: 'Проверить готовность' }));
    await user.click(screen.getByRole('checkbox', { name: /Клапан закрыт/ }));
    await user.click(
      screen.getByRole('checkbox', { name: /Подключение соответствует направлению/ }),
    );
    await user.click(screen.getByRole('button', { name: 'Перейти к проведению' }));
    await user.click(screen.getByRole('button', { name: 'Подтверждаю завершение серии' }));
    expect(screen.getByRole('heading', { name: 'Выход на рабочее давление' })).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'К испытаниям' }));
    await user.click(screen.getByRole('button', { name: 'Вернуться к испытанию' }));
    expect(screen.getByRole('heading', { name: 'Выход на рабочее давление' })).toBeTruthy();
    expect(screen.getByText(/Клапан П-7/)).toBeTruthy();
    const target = screen.getByText('max').closest('.reading');
    if (!(target instanceof HTMLElement)) throw new Error('Target reading missing');
    expect(target.textContent).toMatch(/345,67\s*Па/);
  });

  it('не отображает малое положительное давление как ноль', async () => {
    const user = userEvent.setup();
    render(<App />);
    const newButton = screen.getAllByRole('button', { name: 'Новое испытание' })[0];
    if (!newButton) throw new Error('New test button missing');
    await user.click(newButton);
    await user.type(
      screen.getByRole('textbox', { name: 'Обозначение или наименование клапана' }),
      'Клапан П-8',
    );
    const pmax = screen.getByRole('textbox', { name: 'Pmax производителя, Па' });
    await user.clear(pmax);
    await user.type(pmax, '0,01');
    await user.click(screen.getByRole('button', { name: 'Проверить готовность' }));
    await user.click(screen.getByRole('checkbox', { name: /Клапан закрыт/ }));
    await user.click(
      screen.getByRole('checkbox', { name: /Подключение соответствует направлению/ }),
    );
    await user.click(screen.getByRole('button', { name: 'Перейти к проведению' }));
    const target = screen.getByText('max').closest('.reading');
    if (!(target instanceof HTMLElement)) throw new Error('Target reading missing');
    expect(target.textContent).toMatch(/0,01\s*Па/);
    const pressure = screen.getByText('Давление в ёмкости').closest('.reading');
    if (!(pressure instanceof HTMLElement)) throw new Error('Pressure reading missing');
    expect(pressure.querySelector('strong')?.textContent).not.toMatch(/^0(?:,0+)?\s*Па/);
  });

  it('сообщает ошибку сохранения и не выдаёт её за успешную запись', () => {
    window.history.replaceState(null, '', '/?fixture=save-error');
    render(<App />);
    expect(screen.getByText('Ошибка сохранения · пример')).toBeTruthy();
    expect(screen.getByText('Запись результата не подтверждена.')).toBeTruthy();
    expect(screen.queryByText('Пример: сохранено')).toBeNull();
  });
});
