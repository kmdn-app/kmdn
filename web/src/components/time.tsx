const rtf = new Intl.RelativeTimeFormat("en", { numeric: "auto" });
const df = new Intl.DateTimeFormat("en", { day: "numeric", month: "short", year: "numeric" });

/** "5 minutes ago", "yesterday", or a date for anything older than a month. */
export function relative(iso: string | number | undefined, now = Date.now()): string {
  if (iso === undefined) return "";
  const t = typeof iso === "number" ? iso : Date.parse(iso);
  const s = Math.round((t - now) / 1000);
  const abs = Math.abs(s);
  if (abs < 60) return "just now";
  if (abs < 3600) return rtf.format(Math.round(s / 60), "minute");
  if (abs < 86400) return rtf.format(Math.round(s / 3600), "hour");
  if (abs < 86400 * 30) return rtf.format(Math.round(s / 86400), "day");
  return df.format(t);
}

export function Time({ iso, className }: { iso?: string; className?: string }) {
  if (!iso) return null;
  return (
    <time dateTime={iso} title={new Date(iso).toLocaleString()} className={className}>
      {relative(iso)}
    </time>
  );
}
