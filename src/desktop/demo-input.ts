type DecimalResult =
  | { value: number; error?: never }
  | { value?: never; error: 'invalid' | 'range' | 'precision' };

function canonicalDecimal(value: string): string {
  const [mantissa = '', exponent = '0'] = value.toLowerCase().split('e');
  const [integer = '', fraction = ''] = mantissa.split('.');
  const significant = `${integer}${fraction}`.replace(/^0+/, '');
  const trailing = significant.match(/0+$/)?.[0].length ?? 0;
  const digits = trailing ? significant.slice(0, -trailing) : significant;
  return `${digits}:${Number(exponent) - fraction.length + trailing}`;
}

// Compare decimal values, including the exponent chosen by JSON.stringify, without rounding the input.
export function exactPositiveDecimal(input: string, duration = false): DecimalResult {
  const normalized = input.trim().replace(',', '.');
  if (!/^\d+(?:\.\d+)?$/.test(normalized)) return { error: 'invalid' };
  if (duration) {
    const [rawInteger = '', fraction = ''] = normalized.split('.');
    const integer = rawInteger.replace(/^0+/, '') || '0';
    if (
      integer.length > 4 ||
      (integer.length === 4 && integer > '3600') ||
      (integer === '3600' && /[1-9]/.test(fraction))
    ) {
      return { error: 'range' };
    }
  }
  const value = Number(normalized);
  if (!Number.isFinite(value) || (value === 0 && /[1-9]/.test(normalized)))
    return { error: 'precision' };
  if (value <= 0) return { error: 'invalid' };
  if (canonicalDecimal(normalized) !== canonicalDecimal(JSON.stringify(value)))
    return { error: 'precision' };
  return { value };
}

export function trimDemoValve(value: string): string {
  return value.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, '');
}
