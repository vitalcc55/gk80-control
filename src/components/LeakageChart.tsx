import { useId, useLayoutEffect, useRef } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { formatNumber } from '../lib/format';
import type { LeakagePoint } from '../model';

const height = 550;
const horizontalLabel = 'Удельная утечка, л/(с·м²)';
const verticalLabel = 'Испытательное давление, Па';
const boundaryPressures = [10, 10_000] as const;
const classBoundaries = [
  { coefficient: 15, dash: [] },
  { coefficient: 3, dash: [8, 4] },
  { coefficient: 0.6, dash: [3, 3] },
  { coefficient: 0.12, dash: [8, 3, 2, 3] },
];

function includePoint(min: number, max: number, value?: number) {
  return positiveFinite(value)
    ? {
        min:
          value <= min ? Math.max(Number.MIN_VALUE, 10 ** (Math.ceil(Math.log10(value)) - 1)) : min,
        max:
          value >= max
            ? Math.min(Number.MAX_VALUE, 10 ** (Math.floor(Math.log10(value)) + 1))
            : max,
      }
    : { min, max };
}

function drawBoundaries(chart: uPlot, stroke: string) {
  const ctx = chart.ctx;
  const ratio = uPlot.pxRatio;
  ctx.save();
  ctx.strokeStyle = stroke;
  ctx.lineWidth = 1.5 * ratio;
  for (const { coefficient, dash } of classBoundaries) {
    ctx.setLineDash(dash.map((length) => length * ratio));
    ctx.beginPath();
    for (const [endpoint, pressure] of boundaryPressures.entries()) {
      const x = chart.valToPos(coefficient * pressure ** 0.56, 'x', true);
      const y = chart.valToPos(pressure, 'y', true);
      if (endpoint === 0) ctx.moveTo(x, y);
      else ctx.lineTo(x, y);
    }
    ctx.stroke();
  }
  ctx.restore();
}

function positiveFinite(value: number | undefined): value is number {
  return value !== undefined && Number.isFinite(value) && value > 0;
}

export function LeakageChart({
  point,
  emptyMessage,
}: {
  point?: LeakagePoint | undefined;
  emptyMessage: string;
}) {
  const headingId = useId();
  const plotContainer = useRef<HTMLDivElement>(null);
  const plot = useRef<uPlot | null>(null);
  const leakage = point?.specificLeakageLitresPerSecondPerSquareMetre;
  const pressure = point?.testPressurePa;
  const hasPoint = positiveFinite(leakage) && positiveFinite(pressure);

  useLayoutEffect(() => {
    const container = plotContainer.current;
    if (!container) return;
    const styles = getComputedStyle(container);
    const color = (token: string) => styles.getPropertyValue(token).trim();
    const accent = color('--accent');
    const axis: uPlot.Axis = {
      font: '12px "Medeo Sans", sans-serif',
      labelFont: '12px "Medeo Sans", sans-serif',
      labelSize: 20,
      stroke: color('--chart-axis'),
      grid: { stroke: color('--chart-grid'), width: 1 },
      ticks: { stroke: color('--chart-ticks'), width: 1 },
      values: (_plot, ticks) =>
        ticks.map((tick) => {
          if (!positiveFinite(tick)) return '';
          return tick < 0.01 ? tick.toExponential(0) : formatNumber(tick);
        }),
    };
    const chart = new uPlot(
      {
        width: container.clientWidth,
        height,
        cursor: { show: false },
        legend: { show: false },
        scales: {
          x: { time: false, distr: 3, log: 10, range: (_chart, min, max) => [min, max] },
          y: { distr: 3, log: 10, range: (_chart, min, max) => [min, max] },
        },
        axes: [
          { ...axis, label: horizontalLabel, size: 28 },
          { ...axis, label: verticalLabel, size: 64 },
        ],
        series: [
          {},
          { paths: () => null, points: { show: true, size: 10, fill: accent, stroke: accent } },
        ],
        hooks: { drawClear: [(chart) => drawBoundaries(chart, color('--chart-axis'))] },
      },
      [[], []],
      container,
    );
    plot.current = chart;
    const resize = new ResizeObserver(() => {
      const width = container.clientWidth;
      if (plot.current === chart && width > 0 && width !== chart.width) {
        chart.setSize({ width, height });
      }
    });
    resize.observe(container);
    const refreshFonts = () => {
      if (plot.current === chart) chart.redraw(false, true);
    };
    document.fonts.addEventListener('loadingdone', refreshFonts);
    return () => {
      plot.current = null;
      resize.disconnect();
      document.fonts.removeEventListener('loadingdone', refreshFonts);
      chart.destroy();
    };
  }, []);

  useLayoutEffect(() => {
    const chart = plot.current;
    if (!chart) return;
    chart.batch(() => {
      chart.setData(hasPoint ? [[leakage], [pressure]] : [[], []], false);
      chart.setScale('x', includePoint(0.1, 10_000, hasPoint ? leakage : undefined));
      chart.setScale('y', includePoint(...boundaryPressures, hasPoint ? pressure : undefined));
    });
  }, [hasPoint, leakage, pressure]);

  return (
    <section className="leakage-chart" aria-labelledby={headingId}>
      <div className="section-heading">
        <div>
          <h2 id={headingId}>Характеристика утечки</h2>
          {!hasPoint && <p>{emptyMessage}</p>}
        </div>
      </div>
      <div
        ref={plotContainer}
        className="leakage-chart-plot"
        role="img"
        aria-label={`${horizontalLabel}. ${verticalLabel}. Логарифмические шкалы.`}
      />
      <p className="leakage-chart-source">Границы классов · EN 1751:2024, таблица C.1</p>
      <ul className="leakage-chart-legend" aria-label="Справочные линии">
        {classBoundaries.map(({ coefficient, dash }, index) => (
          <li key={coefficient}>
            <svg width="24" height="8" aria-hidden="true">
              <line
                x1="0"
                y1="4"
                x2="24"
                y2="4"
                stroke="currentColor"
                strokeWidth="1.5"
                strokeDasharray={dash.join(' ')}
              />
            </svg>
            Класс {index + 1}
          </li>
        ))}
      </ul>
      {point && (
        <dl className="leakage-chart-values">
          <div>
            <dt>Удельная утечка</dt>
            <dd>{formatNumber(point.specificLeakageLitresPerSecondPerSquareMetre)} л/(с·м²)</dd>
          </div>
          <div>
            <dt>Испытательное давление</dt>
            <dd>{formatNumber(point.testPressurePa)} Па</dd>
          </div>
        </dl>
      )}
    </section>
  );
}
