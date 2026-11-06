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
export function normalizeHistoryQuery(value: string): string {
  return value.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, '');
}
export interface HistoryRow {
  id: string;
  valve: string;
  createdAt: string | null;
  direction: string;
  pressure: number;
  actuator: boolean;
  outcome: string;
  saving: string;
  active?: boolean;
  activeLabel?: string;
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
  filtered = false,
  retryLabel = 'Повторить чтение списка',
  onRetry,
  onNext,
  onFirst,
  pageSize,
  onPageSize,
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
  filtered?: boolean;
  retryLabel?: string;
  onRetry?: (() => void) | undefined;
  onNext?: (() => void) | undefined;
  onFirst?: () => void;
  pageSize: number;
  onPageSize: (value: number) => void;
  active?: ReactNode;
  children?: ReactNode;
}) {
  const queryTooLong = Array.from(normalizeHistoryQuery(filters.query)).length > 200;
  const dateRangeInvalid = !!filters.from && !!filters.through && filters.from > filters.through;
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
          <button className="table-action" type="button" onClick={onReset}>
            Сбросить поиск
          </button>
        </div>
        <form
          className="history-filters"
          onSubmit={(event) => {
            event.preventDefault();
            if (!queryTooLong) onSearch();
          }}
        >
          <label className="history-search">
            <span>Поиск по номеру или изделию</span>
            <span className="history-search-field">
              <Search aria-hidden="true" size={17} />
              <input
                placeholder="Номер или изделие"
                value={filters.query}
                aria-invalid={queryTooLong || undefined}
                aria-describedby={queryTooLong ? 'history-query-error' : undefined}
                onChange={(e) => onFilters({ ...filters, query: e.target.value })}
              />
            </span>
            {queryTooLong && (
              <span id="history-query-error" role="alert">
                Не более 200 символов без крайних пробелов.
              </span>
            )}
          </label>
          <label>
            С даты
            <input
              type="date"
              value={filters.from}
              aria-invalid={dateRangeInvalid || undefined}
              aria-describedby={dateRangeInvalid ? 'history-date-error' : undefined}
              onChange={(e) => onFilters({ ...filters, from: e.target.value })}
            />
          </label>
          <label>
            По дату
            <input
              type="date"
              value={filters.through}
              aria-invalid={dateRangeInvalid || undefined}
              aria-describedby={dateRangeInvalid ? 'history-date-error' : undefined}
              onChange={(e) => onFilters({ ...filters, through: e.target.value })}
            />
          </label>
          {dateRangeInvalid && (
            <span className="visually-hidden" id="history-date-error">
              Проверьте порядок дат: начало позже конца.
            </span>
          )}
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
        </form>
        {error && (
          <div role="alert">
            <p>{error}</p>
            {onRetry && (
              <button type="button" onClick={onRetry}>
                {retryLabel}
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
              <table className="work-table history-table">
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
                      <td>
                        {row.outcome}
                        {row.active && (
                          <small>{row.activeLabel ?? 'Проход по последнему снимку'}</small>
                        )}
                      </td>
                      <td>{row.saving}</td>
                      <td>
                        <button
                          className="table-action"
                          type="button"
                          aria-label={
                            row.active
                              ? `Вернуться: ${row.activeLabel ?? 'проход по последнему снимку'} ${row.valve} · ${row.id}`
                              : `Открыть ${row.valve} · ${row.id}`
                          }
                          onClick={() => onOpen(row.id)}
                        >
                          {row.active ? 'Вернуться' : 'Открыть'}
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <div className="empty-example">
              <p>{filtered ? 'По заданным условиям записей нет' : 'История пуста'}</p>
            </div>
          ))
        )}
        <div className="history-pages">
          <label className="history-page-size">
            Записей на странице
            <select value={pageSize} onChange={(event) => onPageSize(Number(event.target.value))}>
              {[10, 20, 50, 100].map((size) => (
                <option key={size} value={size}>
                  {size}
                </option>
              ))}
            </select>
          </label>
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
