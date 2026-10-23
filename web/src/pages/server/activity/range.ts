// The time filter of Activity (?when=&from=&to=): a named range of whole days in the reader's
// time zone, or a custom one from one day to another (both included), as the since and until of
// GET /api/audit.

export type When = "" | "today" | "7d" | "30d" | "custom";

export const ranges: { value: Exclude<When, "">; label: string }[] = [
  { value: "today", label: "Today" },
  { value: "7d", label: "Last 7 days" },
  { value: "30d", label: "Last 30 days" },
  { value: "custom", label: "Custom" },
];

export function parseWhen(v: string | null): When {
  return ranges.some((r) => r.value === v) ? (v as When) : "";
}

/** The local midnight that starts the day `days` before today. */
function dayStart(days: number, from = new Date()): Date {
  return new Date(from.getFullYear(), from.getMonth(), from.getDate() - days);
}

/** A YYYY-MM-DD of a date input as the local midnight that starts that day; null when it is not one. */
function parseDay(s: string): Date | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s);
  if (!m) return null;
  const d = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]));
  return isNaN(d.getTime()) ? null : d;
}

/** RFC 3339 in UTC to the second, the form the server reads. */
const rfc3339 = (d: Date) => d.toISOString().replace(/\.\d{3}Z$/, "Z");

/** The local day of d as YYYY-MM-DD, the value of a date input. */
export function dayValue(d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

/** The since and until of a range: the events at or after since and before until. */
export function bounds(when: When, from: string, to: string): { since?: string; until?: string } {
  switch (when) {
    case "today":
      return { since: rfc3339(dayStart(0)) };
    case "7d":
      return { since: rfc3339(dayStart(6)) };
    case "30d":
      return { since: rfc3339(dayStart(29)) };
    case "custom": {
      const a = parseDay(from);
      const b = parseDay(to);
      return { since: a ? rfc3339(a) : undefined, until: b ? rfc3339(dayStart(-1, b)) : undefined };
    }
    default:
      return {};
  }
}

const short = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short" });
const long = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short", year: "numeric" });

/** The words of a range for its filter chip: "Last 7 days", "12 Oct – 20 Oct", "Since 12 Oct". */
export function rangeLabel(when: When, from: string, to: string): string {
  if (when !== "custom") return ranges.find((r) => r.value === when)?.label ?? "";
  const a = parseDay(from);
  const b = parseDay(to);
  const year = new Date().getFullYear();
  const fmt = (d: Date) => (d.getFullYear() === year ? short : long).format(d);
  if (a && b) return dayValue(a) === dayValue(b) ? fmt(a) : `${fmt(a)} – ${fmt(b)}`;
  if (a) return `Since ${fmt(a)}`;
  if (b) return `Until ${fmt(b)}`;
  return "Custom";
}
