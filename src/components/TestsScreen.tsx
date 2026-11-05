import { Plus, Search } from 'lucide-react';
import type { ReactNode } from 'react';
import { formatDateTime, formatNumber } from '../lib/format';
import { PressureNotation } from './PressureNotation';
import './screens.css';

export interface HistoryFilters {
  query: string;
  from: string;
  through: string;
  outcome: string;
}
export const emptyHistoryFilters: HistoryFilters = {
  query: '',
  from: '',
  through: '',
  outcome: '',
};
export interface HistoryRow {
  id: string;
  valve: string;
  createdAt: string | null;
  direction: string;
  pressure: number;
  actuator: boolean;
  outcome: string;
  saving: string;
}

export function TestsScreen({
  rows,
  filters,
  onFilters,
  onSearch,
  onReset,
  onOpen,
  onNew,
  draftAvailable,
  disabled,
  loading = false,
  error = '',
  onRetry,
  onNext,
  onFirst,
  active,
  children,
}: {
  rows: readonly HistoryRow[];
  filters: HistoryFilters;
  onFilters: (value: HistoryFilters) => void;
  onSearch: () => void;
  onReset: () => void;
  onOpen: (id: string) => void;
  onNew: () => void;
  draftAvailable: boolean;
  disabled: boolean;
  loading?: boolean;
  error?: string;
  onRetry?: () => void;
  onNext?: (() => void) | undefined;
  onFirst?: () => void;
  active?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <section className="workpage" aria-labelledby="tests-title">
      <div className="workpage-heading">
        <h1 id="tests-title">Испытания</h1>
        <button
          className="work-button work-button-primary"
          type="button"
          disabled={disabled}
          onClick={onNew}
        >
          <Plus aria-hidden="true" size={18} />
          {draftAvailable ? 'Продолжить подготовку' : 'Новое испытание'}
        </button>
      </div>
      {active}
      {children}
      <div className="work-surface history-surface">
        <div className="surface-heading">
          <h2>История испытаний</h2>
        </div>
        <form
          className="history-filters"
          onSubmit={(event) => {
            event.preventDefault();
            onSearch();
          }}
        >
          <label className="history-search">
            <Search aria-hidden="true" size={17} />
            <span className="visually-hidden">Поиск по номеру или изделию</span>
            <input
              placeholder="Номер или изделие"
              maxLength={200}
              value={filters.query}
              onChange={(e) => onFilters({ ...filters, query: e.target.value })}
            />
          </label>
          <label>
            С даты
            <input
              type="date"
              value={filters.from}
              onChange={(e) => onFilters({ ...filters, from: e.target.value })}
            />
          </label>
          <label>
            По дату
            <input
              type="date"
              value={filters.through}
              onChange={(e) => onFilters({ ...filters, through: e.target.value })}
            />
          </label>
          <label>
            Исход
            <select
              value={filters.outcome}
              onChange={(e) => onFilters({ ...filters, outcome: e.target.value })}
            >
              <option value="">Все исходы</option>
              <option value="completed">Завершено</option>
              <option value="interrupted">Прервано</option>
              <option value="pending">Ожидается</option>
              <option value="unknown">Не подтверждено</option>
            </select>
          </label>
          <button className="work-button work-button-secondary" type="submit">
            Найти
          </button>
          <button className="table-action" type="button" onClick={onReset}>
            Сбросить поиск
          </button>
        </form>
        {error && (
          <div role="alert">
            <p>{error}</p>
            {onRetry && (
              <button type="button" onClick={onRetry}>
                Повторить чтение списка
              </button>
            )}
          </div>
        )}
        {loading ? (
          <p role="status">Чтение истории…</p>
        ) : (
          !error &&
          (rows.length ? (
            <div className="table-scroll">
              <table className="work-table">
                <thead>
                  <tr>
                    <th>Испытание / дата</th>
                    <th>Изделие</th>
                    <th>Направление</th>
                    <th>
                      <PressureNotation />
                    </th>
                    <th>Привод</th>
                    <th>Ход</th>
                    <th>Сохранение</th>
                    <th>
                      <span className="visually-hidden">Действие</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((row) => (
                    <tr key={row.id}>
                      <td>
                        <span className="history-id" title={row.id}>
                          {row.id}
                        </span>
                        <small>
                          {row.createdAt ? formatDateTime(row.createdAt) : 'Дата не задана'}
                        </small>
                      </td>
                      <td>{row.valve}</td>
                      <td>{row.direction}</td>
                      <td>{formatNumber(row.pressure)} Па</td>
                      <td>{row.actuator ? 'Есть' : 'Нет'}</td>
                      <td>{row.outcome}</td>
                      <td>{row.saving}</td>
                      <td>
                        <button
                          className="table-action"
                          type="button"
                          aria-label={`Открыть ${row.valve} · ${row.id}`}
                          onClick={() => onOpen(row.id)}
                        >
                          Открыть
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <div className="empty-example">
              <p>История пуста</p>
              <p>Записей по заданным условиям нет.</p>
            </div>
          ))
        )}
        <div className="history-pages">
          {onFirst && (
            <button className="table-action" type="button" disabled={loading} onClick={onFirst}>
              К началу списка
            </button>
          )}
          {onNext && (
            <button
              className="work-button work-button-secondary"
              type="button"
              disabled={loading}
              onClick={onNext}
            >
              Следующая страница
            </button>
          )}
        </div>
      </div>
    </section>
  );
}
