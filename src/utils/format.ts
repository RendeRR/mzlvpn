export function countryFlag(code: string): string {
  const clean = (code || '').toUpperCase().trim();
  if (clean.length !== 2) return '🌐';
  const first = clean.charCodeAt(0);
  const second = clean.charCodeAt(1);
  if (first < 65 || first > 90 || second < 65 || second > 90) return '🌐';
  return String.fromCodePoint(0x1f1e6 + (first - 65), 0x1f1e6 + (second - 65));
}

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 МБ';
  const gb = bytes / (1024 * 1024 * 1024);
  if (gb >= 1) {
    return `${gb.toFixed(2)} ГБ`;
  }
  const mb = bytes / (1024 * 1024);
  return `${Math.round(mb)} МБ`;
}

export function formatShortDate(iso: string): string {
  try {
    const d = new Date(iso);
    return d.toLocaleDateString('ru-RU', { day: 'numeric', month: 'short' });
  } catch {
    return iso;
  }
}
