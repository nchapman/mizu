// Compact relative-time formatter for stream cards. Returns "just now",
// "5m", "3h", "2d", or a localized date for older items. Returns "" when
// the input is missing or unparseable so the caller can render nothing.
export function relativeTime(iso: string | undefined, now: number = Date.now()): string {
  if (!iso) return "";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  const diff = Math.max(0, (now - t) / 1000);
  if (diff < 30) return "just now";
  if (diff < 3600) return `${Math.floor(diff / 60)}m`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h`;
  if (diff < 86400 * 7) return `${Math.floor(diff / 86400)}d`;
  return new Date(t).toLocaleDateString();
}
