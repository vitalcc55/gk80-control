import { useId, useLayoutEffect, useRef } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { formatNumber } from '../lib/format';
import type { LeakagePoint } from '../model';

const height = 220;
const horizontalLabel = 'Удельная утечка, л/(с·м²)';
const verticalLabel = 'Испытательное давление, Па';

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
      values: (_plot, ticks) => ticks.map((tick) => formatNumber(tick)),
    };
    const chart = new uPlot(
      {
        width: container.clientWidth,
        height,
        cursor: { show: false },
        legend: { show: false },
        scales: {
          x: { time: false, distr: 3, log: 10, range: [1, 100] },
          y: { distr: 3, log: 10, range: [100, 100_000] },
        },
        axes: [
          { ...axis, label: horizontalLabel, size: 28, splits: () => [1, 10, 100] },
          { ...axis, label: verticalLabel, size: 64, splits: () => [100, 1_000, 10_000, 100_000] },
        ],
        series: [
          {},
          { paths: () => null, points: { show: true, size: 10, fill: accent, stroke: accent } },
        ],
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
    plot.current?.setData(hasPoint ? [[leakage], [pressure]] : [[], []]);
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
      {hasPoint && (
        <dl className="leakage-chart-values">
          <div>
            <dt>Удельная утечка</dt>
            <dd>{formatNumber(leakage)} л/(с·м²)</dd>
          </div>
          <div>
            <dt>Испытательное давление</dt>
            <dd>{formatNumber(pressure)} Па</dd>
          </div>
        </dl>
      )}
    </section>
  );
}
