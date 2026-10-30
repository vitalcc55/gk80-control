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
