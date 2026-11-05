import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { StrictMode } from 'react';
import type uPlot from 'uplot';
import { afterEach, beforeEach, expect, it, type Mock, vi } from 'vitest';
import { MeasurementChart } from '../src/components/MeasurementChart';
import type { GraphPoint } from '../src/model';

type PlotStub = {
  options: uPlot.Options;
  data: uPlot.AlignedData;
  axes: uPlot.Axis[];
  root: HTMLDivElement;
  setData: Mock<(data: uPlot.AlignedData, resetScales?: boolean) => void>;
  setScale: Mock<(key: string, limits: { min: number; max: number }) => void>;
  batch: Mock<(update: () => void) => void>;
  setSize: Mock<(size: { width: number; height: number }) => void>;
  redraw: Mock<(rebuildPaths?: boolean, recalcAxes?: boolean) => void>;
  destroy: Mock<() => void>;
};
const plots = vi.hoisted((): { instances: PlotStub[] } => ({ instances: [] }));

vi.mock('uplot', () => ({
  default: class {
    axes: uPlot.Axis[];
    root = document.createElement('div');
    setData = vi.fn((data: uPlot.AlignedData, _resetScales?: boolean) => {
      this.data = data;
    });
    setScale = vi.fn((_key: string, _limits: { min: number; max: number }) => {});
    batch = vi.fn((update: () => void) => update());
    setSize = vi.fn((_size: { width: number; height: number }) => {});
    redraw = vi.fn((_rebuildPaths?: boolean, _recalcAxes?: boolean) => {});
    destroy = vi.fn(() => this.root.remove());
    constructor(
      readonly options: uPlot.Options,
      public data: uPlot.AlignedData,
      container: HTMLElement,
    ) {
      // The real library initializes the default linear-axis callbacks.
      this.axes = (options.axes ?? []).map((axis) => ({
        incrs: () => [1, 2, 5],
        splits: () => [0, 0.5, 1],
        ...axis,
      }));
      container.append(this.root);
      plots.instances.push(this);
    }
  },
}));

class ResizeObserverStub implements ResizeObserver {
  static instances: ResizeObserverStub[] = [];
  observe = vi.fn();
  unobserve = vi.fn();
  disconnect = vi.fn();
  constructor(readonly callback: ResizeObserverCallback) {
    ResizeObserverStub.instances.push(this);
  }
  deliver() {
    this.callback([], this);
  }
}

const fonts = new EventTarget();
let fontsDescriptor: PropertyDescriptor | undefined;
beforeEach(() => {
  plots.instances = [];
  ResizeObserverStub.instances = [];
  fontsDescriptor = Object.getOwnPropertyDescriptor(document, 'fonts');
  Object.defineProperty(document, 'fonts', { configurable: true, value: fonts });
  vi.stubGlobal('ResizeObserver', ResizeObserverStub);
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null);
  vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(640);
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  if (fontsDescriptor) Object.defineProperty(document, 'fonts', fontsDescriptor);
  else Reflect.deleteProperty(document, 'fonts');
});

function axisNumbers(plot: PlotStub, axis: number, splits: number[]): number[] {
  const values = plot.axes[axis]?.values;
  if (typeof values !== 'function') throw new Error('Missing axis formatter');
  // Invoke the external library callback with its controlled plot double.
  const labels: unknown = Reflect.apply(values, undefined, [plot, splits, axis, 75, 1]);
  if (!Array.isArray(labels)) throw new Error('Axis did not return labels');
  return labels.map((label: unknown) => {
    if (typeof label !== 'string') throw new Error('Expected a finite physical label');
    return Number(label.replace(/\s/g, '').replace(',', '.'));
  });
}

function deliver(listener: EventListenerOrEventListenerObject | null | undefined) {
  const event = new Event('loadingdone');
  if (typeof listener === 'function') listener(event);
  else listener?.handleEvent(event);
}

function currentPlot(): PlotStub {
  const plot = plots.instances.at(-1);
  if (!plot) throw new Error('Chart was not constructed');
  return plot;
}

it('reuses number formatters for repeated axis labels without changing physical values', () => {
  render(
    <MeasurementChart
      points={[{ seconds: 0, pressure: 10000, differential: 100 }]}
      target={10000}
    />,
  );
  const formats = vi.spyOn(Intl, 'NumberFormat');
  const plot = currentPlot();
  const values = [9990, 10000, 10010];
  const splits = values.map((value) => value / 10000);
  expect(axisNumbers(plot, 1, splits)).toEqual(values);
  const constructed = formats.mock.calls.length;
  for (let sample = 0; sample < 20; sample++) expect(axisNumbers(plot, 1, splits)).toEqual(values);
  expect(formats).toHaveBeenCalledTimes(constructed);
  formats.mockRestore();
});

it('builds the sample table only while open and updates the same plot while closed', async () => {
  const draw = (pressure: number) => (
    <MeasurementChart points={[{ seconds: 0, pressure, differential: null }]} target={10000} />
  );
  const view = render(draw(1000));
  const plot = currentPlot();
  expect(screen.queryByRole('table')).toBeNull();
  const user = userEvent.setup();
  await user.click(screen.getByText('Таблица показаний'));
  expect((await screen.findByRole('table')).textContent).toContain('Нет данных');
  await user.click(screen.getByText('Таблица показаний'));
  await waitFor(() => expect(screen.queryByRole('table')).toBeNull());
  view.rerender(draw(1001));
  expect(screen.queryByRole('table')).toBeNull();
  expect(currentPlot()).toBe(plot);
  expect(plot.data[1]).toEqual([1001]);
});

it('keeps the chart and subscriptions through twenty new samples after the initial StrictMode cycle', () => {
  const addFont = vi.spyOn(fonts, 'addEventListener');
  const removeFont = vi.spyOn(fonts, 'removeEventListener');
  const draw = (points: GraphPoint[]) => (
    <StrictMode>
      <MeasurementChart points={points} target={10000} />
    </StrictMode>
  );
  const view = render(draw([{ seconds: 0, pressure: 10000, differential: 100 }]));
  const created = plots.instances.length;
  const destroyed = plots.instances.reduce(
    (count, plot) => count + plot.destroy.mock.calls.length,
    0,
  );
  const observers = ResizeObserverStub.instances.length;
  const registrations = addFont.mock.calls.length;
  expect(created - destroyed).toBe(1);
  for (let seconds = 1; seconds <= 20; seconds++) {
    view.rerender(
      draw([
        { seconds: 0, pressure: 10000, differential: 100 },
        { seconds, pressure: 10000 - seconds, differential: 100 + seconds },
      ]),
    );
  }
  expect(plots.instances).toHaveLength(created);
  expect(ResizeObserverStub.instances).toHaveLength(observers);
  expect(addFont).toHaveBeenCalledTimes(registrations);
  expect(removeFont).toHaveBeenCalledTimes(destroyed);
  expect(currentPlot().data[0]).toEqual([0, 20]);
  console.info('MeasurementChart lifecycle', {
    initialConstructors: created,
    initialDestroyed: destroyed,
    sampleUpdates: 20,
    additionalConstructors: plots.instances.length - created,
    additionalDestroyed:
      plots.instances.reduce((count, plot) => count + plot.destroy.mock.calls.length, 0) -
      destroyed,
    additionalObservers: ResizeObserverStub.instances.length - observers,
    additionalFontSubscriptions: addFont.mock.calls.length - registrations,
  });
  view.unmount();
  for (const plot of plots.instances) expect(plot.destroy).toHaveBeenCalledTimes(1);
  for (const observer of ResizeObserverStub.instances)
    expect(observer.disconnect).toHaveBeenCalledTimes(1);
});

it('updates physical axis labels, target, tolerance and raw values together when normalization changes', async () => {
  const points = (pressure: number): GraphPoint[] => [
    { seconds: 0, pressure, differential: 7 },
    { seconds: 5e-101, pressure: null, differential: null },
    { seconds: 1e-100, pressure: pressure * 1.001, differential: 8 },
  ];
  const view = render(
    <MeasurementChart
      points={points(1e-100)}
      target={1e-100}
      tolerance={2}
      rangeSeconds={1e-100}
    />,
  );
  const plot = currentPlot();
  expect(plot.data[0]).toEqual([0, 0.5, 1]);
  expect(plot.data[1]?.[0]).toBe(1);
  expect(plot.data[1]?.[1]).toBeNull();
  expect(plot.data[2]).toEqual([1, 1, 1]);
  expect(plot.data[3]).toEqual([7, null, 8]);
  expect(plot.data[4]).toEqual([1.02, 1.02, 1.02]);
  expect(plot.data[5]).toEqual([0.98, 0.98, 0.98]);
  expect(axisNumbers(plot, 0, [0, 0.5, 1])).toEqual([0, 5e-101, 1e-100]);
  expect(axisNumbers(plot, 1, [1, 1.001])).toEqual([1e-100, 1.001e-100]);
  expect(screen.queryByRole('table')).toBeNull();
  await userEvent.setup().click(screen.getByText('Таблица показаний'));
  expect((await screen.findByRole('table')).textContent).toContain('1E-100');
  expect(screen.getByText('Допуск ±2%')).toBeTruthy();

  // Plotted values stay the same while their physical factor changes by 300 orders.
  view.rerender(
    <MeasurementChart points={points(1e200)} target={1e200} tolerance={5} rangeSeconds={1e-100} />,
  );
  expect(currentPlot()).toBe(plot);
  expect(plots.instances).toHaveLength(1);
  expect(plot.data[1]?.[0]).toBe(1);
  expect(plot.data[4]).toEqual([1.05, 1.05, 1.05]);
  expect(axisNumbers(plot, 1, [1, 1.001])).toEqual([1e200, 1.001e200]);
  expect(plot.redraw).toHaveBeenLastCalledWith(false, true);
  expect(screen.getByRole('table').textContent).toContain('1E200');
  expect(screen.getByText('Допуск ±5%')).toBeTruthy();

  view.rerender(<MeasurementChart points={points(10000)} target={10000} rangeSeconds={1e307} />);
  const splits = plot.axes[0]?.splits;
  const increments = plot.axes[0]?.incrs;
  if (typeof splits !== 'function') throw new Error('Missing x splits');
  if (typeof increments !== 'function') throw new Error('Missing x increments');
  expect(Reflect.apply(increments, undefined, [plot, 0, 0, 1e307, 640, 75])).toEqual([
    2.5e306, 5e306, 1e307,
  ]);
  expect(Reflect.apply(splits, undefined, [plot, 0, 0, 1e307, 1e307 / 4, 75])).toEqual([
    0, 2.5e306, 5e306, 7.5e306, 1e307,
  ]);
  expect(axisNumbers(plot, 0, [0, 5e306, 1e307])).toEqual([0, 5e306, 1e307]);
  expect(plot.data[1]?.[0]).toBe(10000);
  expect(plot.data[4]).toEqual([null, null, null]);
  expect(screen.queryByText(/Допуск ±/)).toBeNull();

  view.rerender(
    <MeasurementChart
      points={[
        { seconds: 0, pressure: 10000, differential: 7 },
        { seconds: 30, pressure: 10010, differential: 8 },
      ]}
      target={10000}
      rangeSeconds={60}
    />,
  );
  expect(Reflect.apply(splits, undefined, [plot, 0, 0, 60, 10, 75])).toEqual([0, 0.5, 1]);
  expect(Reflect.apply(increments, undefined, [plot, 0, 0, 60, 640, 75])).toEqual([1, 2, 5]);
  expect(axisNumbers(plot, 0, [0, 30, 60])).toEqual([0, 30, 60]);
  expect(axisNumbers(plot, 1, [10000, 10010])).toEqual([10000, 10010]);
  expect(plot.setScale).toHaveBeenLastCalledWith('x', { min: 0, max: 60 });
  expect(plot.options.series?.[1]?.spanGaps).toBe(false);
  expect(plot.options.series?.[3]?.spanGaps).toBe(false);
  for (const period of [1e-100, 2e-100]) {
    view.rerender(<MeasurementChart points={points(10000)} target={10000} rangeSeconds={period} />);
    expect(plot.setScale).toHaveBeenLastCalledWith('x', { min: 0, max: 1 });
    expect(axisNumbers(plot, 0, [0, 0.5, 1])).toEqual([0, period / 2, period]);
  }
  view.rerender(<MeasurementChart points={points(10000)} target={10000} />);
  expect(axisNumbers(plot, 0, [0, 30, 60])).toEqual([0, 30, 60]);
  expect(plot.destroy).not.toHaveBeenCalled();
  view.unmount();
});

it('switches normalization for a narrow pressure span and clears an empty auto-range without recreation', () => {
  const view = render(<MeasurementChart points={[]} target={1e-15} />);
  const plot = currentPlot();
  expect(plot.root.style.visibility).toBe('hidden');
  view.rerender(
    <MeasurementChart
      points={[{ seconds: 0, pressure: 1.001e-15, differential: 10 }]}
      target={1e-15}
      live
    />,
  );
  expect(plot.root.style.visibility).toBe('');
  expect(plot.data[1]?.[0]).toBeCloseTo(1.001);
  expect(axisNumbers(plot, 1, [1, 1.001])).toEqual([1e-15, 1.001e-15]);
  expect(screen.getByRole('img', { name: /симулятора/ })).toBeTruthy();
  view.rerender(
    <MeasurementChart points={[{ seconds: 2, pressure: 1, differential: 10 }]} target={1e-15} />,
  );
  expect(plot.data[1]?.[0]).toBe(1);
  expect(axisNumbers(plot, 1, [0, 0.5, 1])).toEqual([0, 0.5, 1]);
  view.rerender(<MeasurementChart points={[]} target={1e-15} />);
  expect(plot.root.style.visibility).toBe('hidden');
  expect(screen.queryByRole('table')).toBeNull();
  view.rerender(<MeasurementChart points={[]} target={1e-15} rangeSeconds={60} />);
  expect(plot.root.style.visibility).toBe('');
  view.rerender(
    <MeasurementChart points={[{ seconds: 3, pressure: 1, differential: 9 }]} target={1e-15} />,
  );
  expect(plot.setData).toHaveBeenLastCalledWith([[3], [1], [1e-15], [9], [null], [null]]);
  expect(plots.instances).toHaveLength(1);
  view.unmount();
});

it('cleans StrictMode observers and fonts and ignores their late callbacks after cleanup', () => {
  const addFont = vi.spyOn(fonts, 'addEventListener');
  const removeFont = vi.spyOn(fonts, 'removeEventListener');
  const view = render(
    <StrictMode>
      <MeasurementChart
        points={[{ seconds: 0, pressure: 10000, differential: 100 }]}
        target={10000}
      />
    </StrictMode>,
  );
  const plot = currentPlot();
  const observer = ResizeObserverStub.instances.at(-1);
  if (!observer) throw new Error('Resize observer is missing');
  observer.deliver();
  expect(plot.setSize).toHaveBeenLastCalledWith({ width: 640, height: 170 });
  fonts.dispatchEvent(new Event('loadingdone'));
  expect(plot.redraw).toHaveBeenLastCalledWith(true, true);
  const first = plots.instances[0];
  if (!first) throw new Error('Missing initial lifecycle');
  if (first !== plot) {
    const draws = first.redraw.mock.calls.length;
    const sizes = first.setSize.mock.calls.length;
    ResizeObserverStub.instances[0]?.deliver();
    deliver(addFont.mock.calls[0]?.[1]);
    expect(first.redraw).toHaveBeenCalledTimes(draws);
    expect(first.setSize).toHaveBeenCalledTimes(sizes);
  }
  const draws = plots.instances.map((instance) => instance.redraw.mock.calls.length);
  const sizes = plots.instances.map((instance) => instance.setSize.mock.calls.length);
  view.unmount();
  for (const resize of ResizeObserverStub.instances) resize.deliver();
  for (const [, listener] of addFont.mock.calls) deliver(listener);
  expect(plots.instances.map((instance) => instance.redraw.mock.calls.length)).toEqual(draws);
  expect(plots.instances.map((instance) => instance.setSize.mock.calls.length)).toEqual(sizes);
  for (const instance of plots.instances) expect(instance.destroy).toHaveBeenCalledTimes(1);
  for (const [event, listener] of addFont.mock.calls)
    expect(removeFont).toHaveBeenCalledWith(event, listener);
});

it('keeps one fallback resize listener and fences callbacks from a previous mount', () => {
  vi.stubGlobal('ResizeObserver', undefined);
  const addResize = vi.spyOn(window, 'addEventListener');
  const removeResize = vi.spyOn(window, 'removeEventListener');
  const points = [{ seconds: 0, pressure: 10000, differential: 100 }];
  const view = render(<MeasurementChart points={points} target={10000} />);
  const plot = currentPlot();
  view.rerender(
    <MeasurementChart
      points={[...points, { seconds: 1, pressure: null, differential: null }]}
      target={10000}
    />,
  );
  const resizeCalls = addResize.mock.calls.filter(([name]) => name === 'resize');
  expect(resizeCalls).toHaveLength(1);
  window.dispatchEvent(new Event('resize'));
  expect(plot.setSize).toHaveBeenCalledTimes(1);
  view.unmount();
  expect(removeResize.mock.calls.filter(([name]) => name === 'resize')).toHaveLength(1);
  const remount = render(<MeasurementChart points={points} target={20000} />);
  for (const [, callback] of resizeCalls) deliver(callback);
  expect(plot.setSize).toHaveBeenCalledTimes(1);
  expect(currentPlot().setSize).not.toHaveBeenCalled();
  expect(plot.destroy).toHaveBeenCalledTimes(1);
  remount.unmount();
  expect(currentPlot().destroy).toHaveBeenCalledTimes(1);
});
