// Text formats shared by every page. Components <FileSize> and <RelativeTime> use these.

const units = ["B", "KiB", "MiB", "GiB", "TiB"];

/** 1536 → "1.5 KiB". */
export function formatSize(n: number): string {
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return i === 0 ? `${n} B` : `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}

const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
const steps: [Intl.RelativeTimeFormatUnit, number][] = [
  ["second", 60],
  ["minute", 60],
  ["hour", 24],
  ["day", 7],
  ["week", 4.35],
  ["month", 12],
  ["year", Infinity],
];

/** "3 minutes ago", "in 2 hours". */
export function formatRelative(iso: string, now = Date.now()): string {
  let v = (new Date(iso).getTime() - now) / 1000;
  for (const [unit, size] of steps) {
    if (Math.abs(v) < size) return rtf.format(Math.round(v), unit);
    v /= size;
  }
  return rtf.format(Math.round(v), "year");
}

const dtf = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

/** Local date and time, e.g. "14 Oct 2026, 09:12". */
export const formatDateTime = (iso: string) => dtf.format(new Date(iso));

/** 12345 → "12,345" in the reader's locale. */
export const formatCount = (n: number) => n.toLocaleString();

/** A key fingerprint in groups of four, as gpg prints it: "A1B2 C3D4 ...". */
export const groupFingerprint = (f: string) => f.toUpperCase().replace(/(.{4})(?=.)/g, "$1 ");
