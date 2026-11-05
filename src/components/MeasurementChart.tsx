import { useLayoutEffect, useRef, useState } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { formatNumber } from '../lib/format';
import type { GraphPoint } from '../model';

interface MeasurementChartProps {
  points: readonly GraphPoint[];
  target: number;
  /** Полный интервал оси времени, если он известен отдельно от наблюдений. */
  rangeSeconds?: number | undefined;
  /** Подготовленная граница допуска для текущего этапа, если она применима. */
  tolerance?: number | undefined;
  live?: boolean;
}

const formatValue = (value: number | null): string =>
  value === null ? 'Нет данных' : formatNumber(value, 12);

/** Выбирает точность по отметкам, чтобы соседние значения не сливались в один текст. */
function formatAxisValues(splits: number[]): string[] {
  const scientific = splits.some(
    (value) => Math.abs(value) > 0 && (Math.abs(value) < 0.001 || Math.abs(value) >= 1_000_000),
  );
  const uniqueCount = new Set(splits).size;
  for (let digits = 3; digits <= 17; digits++) {
    const labels = splits.map((value) =>
      value === 0 ? '0' : formatNumber(value, digits, scientific ? 'scientific' : 'standard'),
    );
    if (new Set(labels).size === uniqueCount) return labels;
  }
  return splits.map((value) => String(value));
}

type ChartProjection = {
  data: uPlot.AlignedData;
  timeScale: number | undefined;
  pressureScale: number | undefined;
  rangeSeconds: number | undefined;
};

function restoredValues(splits: number[], scale: number) {
  const actual = splits.map((value) => value * scale);
  return formatAxisValues(actual).map((label, index) =>
    !Number.isFinite(actual[index]) || (index > 0 && actual[index] === actual[index - 1])
      ? null
      : label,
  );
}

function projectChart({
  points,
  target,
  tolerance,
  rangeSeconds,
}: MeasurementChartProps): ChartProjection {
  // uPlot отвергает x-диапазон менее 1e-16; нормализуем его только для рисования.
  const timeScale =
    rangeSeconds !== undefined && rangeSeconds > 0 && rangeSeconds < 1e-16
      ? rangeSeconds
      : undefined;
  // uPlot теряет шкалу и когда сами значения больше 1e-16, а их размах меньше.
  const pressureRange = points.reduce(
    (range, point) =>
      point.pressure === null
        ? range
        : {
            min: Math.min(range.min, point.pressure),
            max: Math.max(range.max, point.pressure),
            hasMeasurement: true,
          },
    { min: target, max: target, hasMeasurement: false },
  );
  const pressureScale =
    target > 0 &&
    (target <= 1e-16 ||
      target >= 1e150 ||
      (pressureRange.hasMeasurement && pressureRange.max - pressureRange.min < 1e-16))
      ? target
      : undefined;
  const plottedTarget = pressureScale === undefined ? target : target / pressureScale;

  const data: uPlot.AlignedData = [
    points.map((point) => (timeScale === undefined ? point.seconds : point.seconds / timeScale)),
    points.map((point) =>
      point.pressure === null || pressureScale === undefined
        ? point.pressure
        : point.pressure / pressureScale,
    ),
    points.map(() => plottedTarget),
    points.map((point) => point.differential),
    points.map(() => (tolerance === undefined ? null : plottedTarget * (1 + tolerance / 100))),
    points.map(() => (tolerance === undefined ? null : plottedTarget * (1 - tolerance / 100))),
  ];
  return { data, timeScale, pressureScale, rangeSeconds };
}

function updateChart(chart: uPlot, projection: ChartProjection) {
  chart.batch(() => {
    // Штатный reset также обновляет наведённый курсор, даже если шкалы не изменились.
    chart.setData(projection.data);
    if (projection.rangeSeconds !== undefined) {
      chart.setScale('x', {
        min: 0,
        max: projection.timeScale === undefined ? projection.rangeSeconds : 1,
      });
    }
    // setData уже сбросил пути. true здесь вернул бы ожидающий x к прежним границам.
    // Пересчёт осей нужен и при численно одинаковых нормализованных данных.
    chart.redraw(false, true);
  });
  // uPlot сохраняет прежний auto-x при пустом ряде; новый пустой график осей не рисует.
  // Сохраняем этот вид без уничтожения экземпляра и без записи в private scale fields.
  chart.root.style.visibility =
    projection.data[0].length === 0 && projection.rangeSeconds === undefined ? 'hidden' : '';
}

/** График отображает только переданные отсчёты; пропуски остаются разрывами. */
export function MeasurementChart({
  points,
  target,
  tolerance,
  rangeSeconds,
  live = false,
}: MeasurementChartProps) {
  const [tableOpen, setTableOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<uPlot | null>(null);
  const projectionRef = useRef<ChartProjection | null>(null);

  useLayoutEffect(() => {
    const projection = projectChart({ points, target, tolerance, rangeSeconds });
    projectionRef.current = projection;
    if (chartRef.current) updateChart(chartRef.current, projection);
  }, [points, target, tolerance, rangeSeconds]);

  useLayoutEffect(() => {
    const container = containerRef.current;
    const projection = projectionRef.current;
    if (!container || !projection) return;

    // Canvas не разрешает CSS-переменные сам: берём те же значения, что использует легенда.
    const styles = getComputedStyle(container);
    const color = (token: string) => styles.getPropertyValue(token).trim();
    const measurement = container.ownerDocument.createElement('canvas').getContext('2d');
    if (measurement) measurement.font = '12px "Medeo Sans", sans-serif';
    // uPlot вызывает size при инициализации с null, хотя его типы указывают string[].
    const axisWidth = (minimum: number) => (_plot: uPlot, values: string[] | null) =>
      Math.max(
        minimum,
        ...(values ?? []).map((value) =>
          Math.ceil((measurement?.measureText(value).width ?? 0) + 14),
        ),
      );

    const options: uPlot.Options = {
      width: Math.max(container.clientWidth, 180),
      height: 170,
      legend: { show: false },
      cursor: { drag: { x: false, y: false } },
      scales: {
        x: {
          time: false,
          // Фиксированный интервал сохраняется и при штатном autoScaleX по double-click.
          range: (_plot, min, max) => {
            const current = projectionRef.current;
            return current?.rangeSeconds === undefined
              ? [min, max]
              : [0, current.timeScale === undefined ? current.rangeSeconds : 1];
          },
        },
        pressure: { auto: true },
        differential: { auto: true },
      },
      axes: [
        {
          label: 'Время, с',
          // Обе оси линейные: берём подготовленные стандартные шаги uPlot у давления,
          // чтобы не копировать его таблицу и алгоритм для обычного времени.
          incrs: (plot, axis, min, max, size, space) => {
            const range = projectionRef.current?.rangeSeconds;
            if (range !== undefined && range >= 1e32) return [range / 4, range / 2, range];
            const defaults = plot.axes[1]?.incrs;
            if (!defaults) throw new Error('Linear pressure axis is not initialized');
            return typeof defaults === 'function'
              ? defaults(plot, axis, min, max, size, space)
              : defaults;
          },
          splits: (plot, axis, min, max, increment, space) => {
            const range = projectionRef.current?.rangeSeconds;
            if (range !== undefined && range >= 1e32)
              return [0, range * 0.25, range * 0.5, range * 0.75, range];
            const defaults = plot.axes[1]?.splits;
            if (!defaults) throw new Error('Linear pressure axis is not initialized');
            return typeof defaults === 'function'
              ? defaults(plot, axis, min, max, increment, space)
              : defaults;
          },
          values: (_plot, splits) => {
            const scale = projectionRef.current?.timeScale;
            return scale === undefined ? formatAxisValues(splits) : restoredValues(splits, scale);
          },
          space: 75,
          font: '12px "Medeo Sans", sans-serif',
          labelFont: '12px "Medeo Sans", sans-serif',
          labelSize: 22,
          size: 32,
          stroke: color('--chart-axis'),
          grid: { stroke: color('--chart-grid'), width: 1 },
          ticks: { stroke: color('--chart-ticks'), width: 1 },
        },
        {
          scale: 'pressure',
          label: 'Давление, Па',
          values: (_plot, splits) => {
            const scale = projectionRef.current?.pressureScale;
            return scale === undefined ? formatAxisValues(splits) : restoredValues(splits, scale);
          },
          font: '12px "Medeo Sans", sans-serif',
          labelFont: '12px "Medeo Sans", sans-serif',
          labelSize: 20,
          size: axisWidth(56),
          stroke: color('--chart-axis'),
          grid: { stroke: color('--chart-grid'), width: 1 },
          ticks: { stroke: color('--chart-ticks'), width: 1 },
        },
        {
          scale: 'differential',
          side: 1,
          label: 'Перепад, Па',
          values: (_plot, splits) => formatAxisValues(splits),
          font: '12px "Medeo Sans", sans-serif',
          labelFont: '12px "Medeo Sans", sans-serif',
          labelSize: 20,
          size: axisWidth(48),
          stroke: color('--chart-axis'),
          grid: { show: false },
          ticks: { stroke: color('--chart-ticks'), width: 1 },
        },
      ],
      series: [
        {},
        {
          label: 'Давление ДД1',
          scale: 'pressure',
          stroke: color('--chart-pressure'),
          width: 2,
          points: { show: false },
          spanGaps: false,
        },
        {
          label: 'Заданное давление',
          scale: 'pressure',
          stroke: color('--chart-target'),
          width: 1,
          dash: [2, 4],
          points: { show: false },
        },
        {
          label: 'Перепад',
          scale: 'differential',
          stroke: color('--chart-differential'),
          width: 2,
          dash: [7, 3],
          points: { show: false },
          spanGaps: false,
        },
        {
          label: 'Верхняя граница допуска',
          scale: 'pressure',
          stroke: color('--chart-tolerance'),
          width: 1,
          dash: [2, 5],
          points: { show: false },
        },
        {
          label: 'Нижняя граница допуска',
          scale: 'pressure',
          stroke: color('--chart-tolerance'),
          width: 1,
          dash: [2, 5],
          points: { show: false },
        },
      ],
    };

    const chart = new uPlot(options, projection.data, container);
    chartRef.current = chart;
    updateChart(chart, projection);
    let disposed = false;
    const resize = () => {
      if (disposed) return;
      const width = container.clientWidth;
      if (width > 0) chart.setSize({ width: Math.max(width, 180), height: 170 });
    };
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(resize);
    if (observer) observer.observe(container);
    else window.addEventListener('resize', resize);
    const refreshFonts = () => {
      if (!disposed) chart.redraw(true, true);
    };
    document.fonts?.addEventListener('loadingdone', refreshFonts);

    return () => {
      disposed = true;
      chartRef.current = null;
      observer?.disconnect();
      if (!observer) window.removeEventListener('resize', resize);
      document.fonts?.removeEventListener('loadingdone', refreshFonts);
      chart.destroy();
    };
  }, []);

  return (
    <div>
      <div
        ref={containerRef}
        className="chart-container"
        role="img"
        aria-label={
          live
            ? 'График показаний симулятора по времени; разрывы обозначают отсутствие данных'
            : 'График подготовленных показаний давления и перепада по времени; разрывы обозначают отсутствие данных'
        }
      />
      <div className="chart-legend">
        <span>
          <i className="chart-key chart-key-pressure" /> Давление ДД1, Па
        </span>
        <span>
          <i className="chart-key chart-key-target" /> Заданное, Па
        </span>
        <span>
          <i className="chart-key chart-key-differential" /> Перепад, Па
        </span>
        {tolerance !== undefined && <span>Допуск ±{tolerance}%</span>}
      </div>
      <details className="chart-data" onToggle={(event) => setTableOpen(event.currentTarget.open)}>
        <summary>Таблица показаний</summary>
        {tableOpen && (
          <div className="chart-data-scroll">
            <table className="chart-data-table">
              <caption className="visually-hidden">
                {live ? 'Показания симулятора по времени' : 'Подготовленные показания по времени'}
              </caption>
              <thead>
                <tr>
                  <th scope="col">Время, с</th>
                  <th scope="col">Давление ДД1, Па</th>
                  <th scope="col">Перепад, Па</th>
                </tr>
              </thead>
              <tbody>
                {points.map((point) => (
                  <tr key={point.seconds}>
                    <td>{formatValue(point.seconds)}</td>
                    <td>{formatValue(point.pressure)}</td>
                    <td>{formatValue(point.differential)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </details>
    </div>
  );
}
