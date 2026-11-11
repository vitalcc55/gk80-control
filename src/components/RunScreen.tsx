import { Check } from 'lucide-react';
import type { ComponentProps, ReactNode, Ref } from 'react';
import { MeasurementChart } from './MeasurementChart';
import { PressureNotation } from './PressureNotation';

export function RunMeasurementChart({
  note,
  ...chart
}: ComponentProps<typeof MeasurementChart> & { note?: ReactNode }) {
  return (
    <>
      <div className="section-heading">
        <div>
          <h2>Изменение показаний</h2>
          {note && <p>{note}</p>}
        </div>
      </div>
      <MeasurementChart {...chart} />
    </>
  );
}

export function RunScreen({
  subtitle,
  stages,
  pressure,
  differential,
  differentialNote,
  target,
  readingNote,
  pressureNote,
  details,
  chart,
  actionTitle,
  instruction,
  actionLabel,
  time,
  action,
  stop,
  headingRef,
}: {
  subtitle: ReactNode;
  stages: readonly { id: string; label: string; state: 'done' | 'current' | 'next' | 'inactive' }[];
  pressure: ReactNode;
  differential: ReactNode;
  target: ReactNode;
  readingNote?: ReactNode;
  details?: ReactNode;
  pressureNote?: ReactNode;
  differentialNote?: ReactNode;
  chart: ReactNode;
  actionTitle: string;
  instruction?: string | undefined;
  actionLabel?: string;
  time?: ReactNode;
  action?: ReactNode;
  stop?: ReactNode;
  headingRef?: Ref<HTMLHeadingElement>;
}) {
  return (
    <section aria-labelledby="run-title">
      <div className="page-heading run-heading">
        <div className="run-heading-content">
          <div className="run-heading-row">
            <h1 id="run-title">Проведение испытания</h1>
            <p>{subtitle}</p>
          </div>
        </div>
      </div>
      <div className="run-grid">
        <aside className="run-stages" aria-label="Этапы испытания">
          <div className="section-heading">
            <h2>Этапы</h2>
          </div>
          <ol className="stage-list">
            {stages.map((stage, index) => (
              <li
                key={stage.id}
                className={stage.state === 'current' ? 'stage current' : 'stage'}
                aria-current={stage.state === 'current' ? 'step' : undefined}
              >
                <span className="stage-marker">
                  {stage.state === 'done' ? <Check size={13} aria-hidden="true" /> : index + 1}
                </span>
                <span>
                  <strong>{stage.label}</strong>
                  <small className="sr-only">
                    {stage.state === 'current'
                      ? 'Текущий этап'
                      : stage.state === 'done'
                        ? 'Предыдущий этап'
                        : stage.state === 'inactive'
                          ? 'Не выполняется'
                          : 'Ожидается'}
                  </small>
                </span>
              </li>
            ))}
          </ol>
        </aside>
        <section className="run-content" aria-label="Измерения">
          <div className="measurement-top">
            <div className="section-heading">
              <h2>Показания</h2>
            </div>
            {readingNote && <p className="reading-note">{readingNote}</p>}
            <div className="measurements live-measurements">
              <div className="reading main-reading">
                <span>
                  Давление в ёмкости <small>ДД1</small>
                </span>
                <strong>{pressure}</strong>
                {pressureNote}
              </div>
              <div className="reading target-reading">
                <span>
                  <PressureNotation /> производителя
                </span>
                <strong>{target}</strong>
              </div>
              <div className="reading differential-reading">
                <span>
                  Перепад на диафрагме <small>ДД2</small>
                </span>
                <strong>{differential}</strong>
                {differentialNote}
              </div>
            </div>
            {details}
          </div>
        </section>
        <aside className="action-panel" aria-label={actionLabel ?? 'Текущее действие'}>
          <div>
            <h2 ref={headingRef} tabIndex={-1}>
              {actionTitle}
            </h2>
            {instruction && <p className="action-instruction">{instruction}</p>}
            {time}
            {action}
          </div>
          {stop && <div className="stop-area">{stop}</div>}
        </aside>
        <div className="chart-panel">{chart}</div>
      </div>
    </section>
  );
}
