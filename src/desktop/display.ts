export function outcomeLabel(value: string): string {
  switch (value) {
    case 'completed':
      return 'Завершено';
    case 'interrupted':
      return 'Прервано';
    case 'pending':
      return 'Ожидается';
    default:
      return 'Не подтверждено';
  }
}

export function qualityLabel(value: string): string {
  switch (value) {
    case 'complete':
      return 'Полные';
    case 'incomplete':
      return 'Неполные';
    case 'pending':
      return 'Ожидаются';
    default:
      return 'Не подтверждено';
  }
}

export function saveLabel(value: string): string {
  switch (value) {
    case 'saved':
      return 'Сохранено';
    case 'failed':
      return 'Ошибка сохранения';
    case 'pending':
      return 'Сохранение выполняется';
    default:
      return 'Не подтверждено';
  }
}

export function measurementQualityLabel(value: string | undefined): string {
  switch (value) {
    case 'valid':
      return 'достоверно';
    case 'stale':
      return 'устарело';
    case 'unavailable':
      return 'недоступно';
    default:
      return 'неизвестно';
  }
}

export function measurementSourceLabel(value: string): string {
  return value === 'simulator' ? 'симулятор' : 'неизвестно';
}

const eventActions = new Map([
  ['create', 'Создание испытания'],
  ['start', 'Запуск испытания'],
  ['confirm', 'Подтверждение действия'],
  ['complete', 'Завершение испытания'],
  ['recovered', 'Восстановление записи'],
  ['stop', 'Остановка испытания'],
]);
const eventOutcomes = new Map([
  ['accepted', 'Принято'],
  ['completed', 'Выполнено'],
  ['rejected', 'Отклонено'],
  ['failed', 'Ошибка выполнения'],
  ['unknown', 'Результат неизвестен'],
]);
const eventReasons = new Map([
  ['operator', 'Остановка по запросу оператора'],
  ['ui_lost', 'Управление из интерфейса завершено или потеряно'],
  ['owner_lost', 'Работа ядра завершена'],
  ['storage_error', 'Состояние сохранения не подтверждено'],
  ['simulator_error', 'Ошибка выполнения испытания'],
  ['measurement_queue_full:3', 'Очередь записи измерений переполнена'],
  ['measurement_write_timeout', 'Срок ожидания записи измерений истёк'],
  ['measurement_write_failed', 'Ошибка записи измерений'],
  ['simulator_stop_failed', 'Остановка не подтверждена'],
  ['canceled_by_stop', 'Запуск отменён при остановке до подтверждения оператора'],
  ['invalid_command', 'Некорректная команда'],
  ['revision', 'Состояние испытания изменилось'],
  ['invalid_state', 'Действие недоступно в текущем состоянии'],
  ['action_mismatch', 'Действие не соответствует ожидаемому'],
  ['test_not_found', 'Испытание не найдено'],
  ['not_ready', 'Готовность не подтверждена'],
]);

export function eventActionLabel(value: string): string {
  return eventActions.get(value) ?? 'Неизвестное событие';
}

export function eventOutcomeLabel(value: string): string {
  return eventOutcomes.get(value) ?? 'Неизвестный результат';
}

export function eventReasonLabels(value: string): string[] {
  return value.split(';').map((part) => eventReasons.get(part) ?? 'Неизвестная причина');
}

export function eventActorLabel(value: string): string {
  return value === 'demo-operator'
    ? 'Оператор'
    : value === 'system'
      ? 'Система'
      : 'Неизвестный участник';
}
