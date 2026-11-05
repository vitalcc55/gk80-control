import type { ReactNode } from 'react';
export function ActiveTestPanel({
  title,
  status,
  description,
  onReturn,
  stop,
}: {
  title: string;
  status: string;
  description: ReactNode;
  onReturn: () => void;
  stop: ReactNode;
}) {
  return (
    <aside className="action-panel active-route-panel" aria-label="Активное испытание">
      <div>
        <span className="status-label">
          <span className="status-dot" />
          {status}
        </span>
        <h2>{title}</h2>
        <p>{description}</p>
        <button className="work-button work-button-secondary" type="button" onClick={onReturn}>
          Вернуться к испытанию
        </button>
      </div>
      <div className="stop-area">{stop}</div>
    </aside>
  );
}
