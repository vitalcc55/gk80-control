import { useState } from 'react';
import {
  emptyHistoryFilters,
  type HistoryFilters,
  normalizeHistoryQuery,
  TestsScreen,
} from './components/TestsScreen';
import type { TestParameters } from './model';
import './components/screens.css';

interface TestsScreenProps {
  empty: boolean;
  showExample: boolean;
  draftAvailable: boolean;
  activeParameters: TestParameters | null;
  history: readonly TestParameters[];
  example: TestParameters;
  onNew: () => void;
  onHistory: (parameters: TestParameters) => void;
  onCurrent: () => void;
}

export function FixtureTests({
  empty,
  showExample,
  draftAvailable,
  activeParameters,
  history,
  example,
  onNew,
  onHistory,
  onCurrent,
}: TestsScreenProps) {
  const [filters, setFilters] = useState<HistoryFilters>(emptyHistoryFilters);
  const [applied, setApplied] = useState<HistoryFilters>(emptyHistoryFilters);
  const [rangeError, setRangeError] = useState(false);
  const query = normalizeHistoryQuery(applied.query);
  const matchQuery = new RegExp(query.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'iu');
  const entries = empty
    ? []
    : [
        ...history.filter((item) => item.id !== activeParameters?.id),
        ...(showExample &&
        example.id !== activeParameters?.id &&
        !history.some((item) => item.id === example.id)
          ? [example]
          : []),
      ];
  const matches = entries.filter(
    (item) =>
      [item.id, item.valve].some((value) => matchQuery.test(value)) &&
      !applied.from &&
      !applied.through &&
      (!applied.outcome || applied.outcome === 'completed'),
  );
  const activeMatches =
    activeParameters &&
    [activeParameters.id, activeParameters.valve].some((value) => matchQuery.test(value)) &&
    !applied.from &&
    !applied.through &&
    (!applied.outcome || applied.outcome === 'pending');

  return (
    <TestsScreen
      rows={[
        ...(activeMatches && activeParameters
          ? [
              {
                id: activeParameters.id,
                valve: activeParameters.valve,
                createdAt: null,
                direction: activeParameters.direction === 'pressure' ? 'Нагнетание' : 'Разрежение',
                pressure: activeParameters.pmax,
                actuator: activeParameters.actuator,
                outcome: 'Пример: не завершён',
                saving: 'Пример',
                active: true,
                activeLabel: 'Активный пример',
              },
            ]
          : []),
        ...matches.map((entry) => ({
          id: entry.id,
          valve: entry.valve,
          createdAt: null,
          direction: entry.direction === 'pressure' ? 'Нагнетание' : 'Разрежение',
          pressure: entry.pmax,
          actuator: entry.actuator,
          outcome: 'Завершён в примере',
          saving: 'Пример: сохранено',
        })),
      ]}
      filters={filters}
      filtered={!!(query || applied.from || applied.through || applied.outcome)}
      error={rangeError ? 'Начало диапазона должно быть не позже конца.' : ''}
      onFilters={setFilters}
      onSearch={() => {
        const invalid = !!filters.from && !!filters.through && filters.from > filters.through;
        setRangeError(invalid);
        setApplied(invalid ? emptyHistoryFilters : filters);
      }}
      onReset={() => {
        setFilters(emptyHistoryFilters);
        setApplied(emptyHistoryFilters);
        setRangeError(false);
      }}
      onOpen={(id) => {
        if (id === activeParameters?.id) {
          onCurrent();
          return;
        }
        const entry = entries.find((item) => item.id === id);
        if (entry) onHistory(entry);
      }}
      onNew={onNew}
      disabled={activeParameters !== null}
      draftAvailable={draftAvailable}
    />
  );
}
