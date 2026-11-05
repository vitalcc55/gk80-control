import { useState } from 'react';
import { emptyHistoryFilters, type HistoryFilters, TestsScreen } from './components/TestsScreen';
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
}: TestsScreenProps) {
  const [filters, setFilters] = useState<HistoryFilters>(emptyHistoryFilters);
  const query = filters.query;
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
      `${item.id} ${item.valve}`
        .toLocaleLowerCase('ru')
        .includes(query.trim().toLocaleLowerCase('ru')) &&
      !filters.from &&
      !filters.through &&
      (!filters.outcome || filters.outcome === 'completed'),
  );

  return (
    <TestsScreen
      rows={matches.map((entry) => ({
        id: entry.id,
        valve: entry.valve,
        createdAt: null,
        direction: entry.direction === 'pressure' ? 'Нагнетание' : 'Разрежение',
        pressure: entry.pmax,
        actuator: entry.actuator,
        outcome: 'Завершён в примере',
        saving: 'Пример: сохранено',
      }))}
      filters={filters}
      onFilters={setFilters}
      onSearch={() => {}}
      onReset={() => setFilters(emptyHistoryFilters)}
      onOpen={(id) => {
        const entry = entries.find((item) => item.id === id);
        if (entry) onHistory(entry);
      }}
      onNew={onNew}
      disabled={activeParameters !== null}
      draftAvailable={draftAvailable}
    />
  );
}
