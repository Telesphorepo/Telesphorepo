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

const plural = (n: number, one: string, many: string) => `${n.toLocaleString()} ${n === 1 ? one : many}`;

/** A length of time in words: "40 seconds", "6 minutes", "2 hours 5 minutes". */
export function formatDuration(ms: number): string {
  const sec = Math.max(0, Math.round(ms / 1000));
  if (sec < 60) return plural(sec, "second", "seconds");
  const min = Math.round(sec / 60);
  if (min < 60) return plural(min, "minute", "minutes");
  const h = Math.floor(min / 60);
  const m = min % 60;
  return m ? `${plural(h, "hour", "hours")} ${plural(m, "minute", "minutes")}` : plural(h, "hour", "hours");
}

/** A short length of time for a value that ticks: "4 s", "6 min", "2 h 5 min". */
export function formatSpan(ms: number): string {
  const sec = Math.max(0, Math.round(ms / 1000));
  if (sec < 60) return `${sec} s`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min} min`;
  const h = Math.floor(min / 60);
  return min % 60 ? `${h} h ${min % 60} min` : `${h} h`;
}

/** An interval after "every", from seconds: "3 seconds", "10 minutes", "hour", "2 hours". */
export function formatEvery(seconds: number): string {
  if (seconds >= 3600 && seconds % 3600 === 0) return seconds === 3600 ? "hour" : plural(seconds / 3600, "hour", "hours");
  if (seconds >= 60 && seconds % 60 === 0) return seconds === 60 ? "minute" : plural(seconds / 60, "minute", "minutes");
  return seconds === 1 ? "second" : plural(seconds, "second", "seconds");
}

const dtf = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

/** Local date and time, e.g. "14 Oct 2026, 09:12". */
export const formatDateTime = (iso: string) => dtf.format(new Date(iso));

/** 12345 → "12,345" in the reader's locale. */
export const formatCount = (n: number) => n.toLocaleString();

/** A key fingerprint in groups of four, as gpg prints it: "A1B2 C3D4 ...". */
export const groupFingerprint = (f: string) => f.toUpperCase().replace(/(.{4})(?=.)/g, "$1 ");
