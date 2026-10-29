import { useEffect, useRef } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import type { GraphPoint } from '../model';

interface MeasurementChartProps {
  points: readonly GraphPoint[];
  target: number;
  /** Подготовленная граница допуска для текущего этапа, если она применима. */
  tolerance?: number | undefined;
}

/** График отображает только переданные отсчёты; пропуски остаются разрывами. */
export function MeasurementChart({ points, target, tolerance }: MeasurementChartProps) {
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    const data: uPlot.AlignedData = [
      points.map((point) => point.seconds),
      points.map((point) => point.pressure),
      points.map(() => target),
      points.map((point) => point.differential),
      points.map(() => (tolerance === undefined ? null : target * (1 + tolerance / 100))),
      points.map(() => (tolerance === undefined ? null : target * (1 - tolerance / 100))),
    ];
    const options: uPlot.Options = {
      width: Math.max(container.clientWidth, 180),
      height: 170,
      legend: { show: false },
      cursor: { drag: { x: false, y: false } },
      scales: {
        x: { time: false },
        pressure: { auto: true },
        differential: { auto: true },
      },
      axes: [
        {
          label: 'Время, с',
          font: '11px "Medeo Sans", sans-serif',
          labelFont: '12px "Medeo Sans", sans-serif',
          labelSize: 22,
          size: 32,
          stroke: '#536a6c',
          grid: { stroke: '#e5ebe8', width: 1 },
          ticks: { stroke: '#ccd9d5', width: 1 },
        },
        {
          scale: 'pressure',
          label: 'Давление, Па',
          font: '11px "Medeo Sans", sans-serif',
          labelFont: '12px "Medeo Sans", sans-serif',
          labelSize: 20,
          size: 56,
          stroke: '#536a6c',
          grid: { stroke: '#e5ebe8', width: 1 },
          ticks: { stroke: '#ccd9d5', width: 1 },
        },
        {
          scale: 'differential',
          side: 1,
          label: 'Перепад, Па',
          font: '11px "Medeo Sans", sans-serif',
          labelFont: '12px "Medeo Sans", sans-serif',
          labelSize: 20,
          size: 48,
          stroke: '#80613f',
          grid: { show: false },
          ticks: { stroke: '#d7c8b8', width: 1 },
        },
      ],
      series: [
        {},
        {
          label: 'Давление ДД1',
          scale: 'pressure',
          stroke: '#277772',
          width: 2,
          points: { show: false },
          spanGaps: false,
        },
        {
          label: 'Заданное давление',
          scale: 'pressure',
          stroke: '#71888a',
          width: 1,
          dash: [5, 4],
          points: { show: false },
        },
        {
          label: 'Перепад',
          scale: 'differential',
          stroke: '#ad7544',
          width: 2,
          points: { show: false },
          spanGaps: false,
        },
        {
          label: 'Верхняя граница допуска',
          scale: 'pressure',
          stroke: '#a88758',
          width: 1,
          dash: [2, 5],
          points: { show: false },
        },
        {
          label: 'Нижняя граница допуска',
          scale: 'pressure',
          stroke: '#a88758',
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
  }, [points, target, tolerance]);

  return (
    <div>
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
      <div
        ref={containerRef}
        className="chart-container"
        role="img"
        aria-label="График подготовленных показаний давления и перепада по времени; разрывы обозначают отсутствие данных"
      />
    </div>
  );
}
