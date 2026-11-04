import { type ComponentProps, StrictMode, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { MeasurementChart } from '../../src/components/MeasurementChart';
import '../../src/index.css';

type ChartProps = ComponentProps<typeof MeasurementChart>;
const normal: ChartProps = {
  points: [
    { seconds: 0, pressure: 9720, differential: 70 },
    { seconds: 15, pressure: 10040, differential: 84 },
    { seconds: 30, pressure: 10010, differential: 83 },
  ],
  target: 10000,
  tolerance: 2,
  rangeSeconds: 60,
};
const scaled = (target: number, rangeSeconds: number): ChartProps => ({
  points: [
    { seconds: 0, pressure: target, differential: 70 },
    { seconds: rangeSeconds / 4, pressure: null, differential: null },
    { seconds: rangeSeconds / 2, pressure: target * 1.001, differential: 84 },
  ],
  target,
  tolerance: 5,
  rangeSeconds,
});
const examples: readonly { label: string; props: ChartProps }[] = [
  { label: 'Обычный', props: normal },
  { label: 'Малый', props: scaled(1e-100, 1e-100) },
  { label: 'Большой', props: scaled(1e200, 1e307) },
  { label: 'Узкий', props: scaled(1e-15, 60) },
  { label: 'Авто', props: { points: normal.points, target: normal.target } },
  { label: 'Пустой', props: { points: [], target: normal.target } },
  {
    label: 'Разрыв',
    props: {
      ...normal,
      points: [...normal.points.slice(0, 2), { seconds: 30, pressure: null, differential: null }],
    },
  },
];

function ChartExample() {
  const [props, setProps] = useState(normal);
  return (
    <main style={{ padding: 24, maxWidth: 1000 }}>
      <p>Только проверка реального компонента · подготовленные данные</p>
      <div>
        {examples.map((example) => (
          <button type="button" key={example.label} onClick={() => setProps(example.props)}>
            {example.label}
          </button>
        ))}
      </div>
      <MeasurementChart {...props} />
    </main>
  );
}

const root = document.getElementById('root');
if (!root) throw new Error('Chart test root is missing');
createRoot(root).render(
  <StrictMode>
    <ChartExample />
  </StrictMode>,
);
