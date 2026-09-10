export function numeric(value: number | null, render: (n: number) => string): string {
  if (value === null) return '—';
  // JSON integers beyond JavaScript's exact range must not look authoritative.
  if (!Number.isSafeInteger(value) || value < 0) return 'Outside display range';
  return render(value);
}
export const duration = (value: number | null) => numeric(value, n => n < 1000000 ? (n / 1000).toFixed(3) + ' ms' : (n / 1000000).toFixed(3) + ' s');
export const tokens = (value: number | null) => numeric(value, n => n.toLocaleString('en-US'));
export const cost = (value: number | null) => numeric(value, n => '$' + Math.floor(n / 1000000) + '.' + String(n % 1000000).padStart(6, '0'));
export const timestamp = (value: string) => value.replace('T', ' ').replace(/Z$/, ' UTC');
