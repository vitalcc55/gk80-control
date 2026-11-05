const numberFormats = new Map<string, Intl.NumberFormat>();
const dateFormat = new Intl.DateTimeFormat('ru-RU', { dateStyle: 'short', timeStyle: 'short' });

/** Display precision never changes the underlying measurements. At most 42 formatter variants. */
export function formatNumber(
  value: number,
  maximumSignificantDigits = 21,
  notationOverride?: 'standard' | 'scientific',
): string {
  if (
    !Number.isInteger(maximumSignificantDigits) ||
    maximumSignificantDigits < 1 ||
    maximumSignificantDigits > 21
  )
    throw new RangeError('Invalid display precision');
  const magnitude = Math.abs(value);
  const notation =
    notationOverride ??
    (magnitude >= 1e12 || (magnitude > 0 && magnitude < 1e-9) ? 'scientific' : 'standard');
  const key = `${notation}:${maximumSignificantDigits}`;
  let formatter = numberFormats.get(key);
  if (!formatter) {
    formatter = new Intl.NumberFormat('ru-RU', { notation, maximumSignificantDigits });
    numberFormats.set(key, formatter);
  }
  return formatter.format(value);
}

export function formatDateTime(value: string): string {
  const timestamp = Date.parse(value);
  return Number.isFinite(timestamp) ? dateFormat.format(timestamp) : 'Время неизвестно';
}

export function formatElapsedSeconds(value: number): string {
  return formatNumber(Math.floor(value));
}

export function formatRemainingSeconds(value: number): string {
  return formatNumber(Math.ceil(value));
}
