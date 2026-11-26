const numberFormat = new Intl.NumberFormat('ru-RU', { maximumFractionDigits: 2 });
const scientificFormat = new Intl.NumberFormat('ru-RU', {
  notation: 'scientific',
  maximumFractionDigits: 2,
});
const dateFormat = new Intl.DateTimeFormat('ru-RU', { dateStyle: 'short', timeStyle: 'short' });
const eventDateFormat = new Intl.DateTimeFormat('ru-RU', {
  dateStyle: 'short',
  timeStyle: 'medium',
});

export const unavailableValue = '—';

/** Format the displayed reading without changing the stored or charted measurement. */
export function formatMeasurement(value: number | null): string {
  if (value === null) return unavailableValue;
  if (value > 0 && value < 0.005) return '< 0,01 Па';
  if (value < 0 && value > -0.005) return '−0,01 < значение < 0 Па';
  return `≈ ${formatNumber(value)} Па`;
}

/** Display precision never changes the underlying measurements. */
export function formatNumber(value: number): string {
  const magnitude = Math.abs(value);
  const formatter =
    magnitude >= 1e12 || (magnitude > 0 && magnitude < 0.005) ? scientificFormat : numberFormat;
  return formatter.format(Object.is(value, -0) ? 0 : value);
}

export function formatDateTime(value: string): string {
  const timestamp = Date.parse(value);
  return Number.isFinite(timestamp) ? dateFormat.format(timestamp) : 'Время неизвестно';
}

export function formatEventDateTime(value: string): string {
  const timestamp = Date.parse(value);
  return Number.isFinite(timestamp) ? eventDateFormat.format(timestamp) : 'Время неизвестно';
}

export function formatElapsedSeconds(value: number): string {
  return formatNumber(Math.floor(value));
}

export function formatRemainingSeconds(value: number): string {
  return formatNumber(Math.ceil(value));
}
