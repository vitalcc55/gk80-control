import { useEffect, useRef } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { formatNumber } from '../fixtureAdapter';
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
    const formatter = new Intl.NumberFormat('ru-RU', {
      notation: scientific ? 'scientific' : 'standard',
      maximumSignificantDigits: digits,
    });
    const labels = splits.map((value) => (value === 0 ? '0' : formatter.format(value)));
    if (new Set(labels).size === uniqueCount) return labels;
  }
  return splits.map((value) => String(value));
}

/** График отображает только переданные отсчёты; пропуски остаются разрывами. */
export function MeasurementChart({
  points,
  target,
  tolerance,
  rangeSeconds,
  live = false,
}: MeasurementChartProps) {
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

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
    const restoredValues = (splits: number[], scale: number) => {
      const actual = splits.map((value) => value * scale);
      return formatAxisValues(actual).map((label, index) =>
        !Number.isFinite(actual[index]) || (index > 0 && actual[index] === actual[index - 1])
          ? null
          : label,
      );
    };
    const timeValues = (_plot: uPlot, splits: number[]) => {
      if (timeScale === undefined) return formatAxisValues(splits);
      return restoredValues(splits, timeScale);
    };
    const plottedTarget = pressureScale === undefined ? target : target / pressureScale;

    // Штатная шкала uPlot не подбирает шаги на диапазонах выше её таблицы инкрементов.
    const wideTimeAxis =
      rangeSeconds !== undefined && rangeSeconds >= 1e32
        ? {
            incrs: [rangeSeconds / 4, rangeSeconds / 2, rangeSeconds],
            splits: () => [
              0,
              rangeSeconds * 0.25,
              rangeSeconds * 0.5,
              rangeSeconds * 0.75,
              rangeSeconds,
            ],
          }
        : {};

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
    const options: uPlot.Options = {
      width: Math.max(container.clientWidth, 180),
      height: 170,
      legend: { show: false },
      cursor: { drag: { x: false, y: false } },
      scales: {
        x: {
          time: false,
          ...(rangeSeconds === undefined
            ? {}
            : { range: [0, timeScale === undefined ? rangeSeconds : 1] }),
        },
        pressure: { auto: true },
        differential: { auto: true },
      },
      axes: [
        {
          label: 'Время, с',
          ...wideTimeAxis,
          values: timeValues,
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
          values: (_plot, splits) =>
            pressureScale === undefined
              ? formatAxisValues(splits)
              : restoredValues(splits, pressureScale),
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

    const chart = new uPlot(options, data, container);
    const resize = () => {
      const width = container.clientWidth;
      if (width > 0) chart.setSize({ width: Math.max(width, 180), height: 170 });
    };
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(resize);
    if (observer) observer.observe(container);
    else window.addEventListener('resize', resize);
    const refreshFonts = () => chart.redraw();
    document.fonts?.addEventListener('loadingdone', refreshFonts);

    return () => {
      observer?.disconnect();
      if (!observer) window.removeEventListener('resize', resize);
      document.fonts?.removeEventListener('loadingdone', refreshFonts);
      chart.destroy();
    };
  }, [points, target, tolerance, rangeSeconds]);

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
      <details className="chart-data">
        <summary>Таблица показаний</summary>
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
      </details>
    </div>
  );
}
