/**
 * Number-plus-unit editing of size and duration settings. The server stores a size as bytes and a
 * duration as Go text ("2160h"); the form shows a number and a unit and sends text like "512MiB"
 * or "90d", which the server parses.
 */

export type Unit = { value: string; label: string; factor: number };
export type Parts = { num: string; unit: string };

export const SIZE_UNITS: Unit[] = [
  { value: "KiB", label: "KiB", factor: 1024 },
  { value: "MiB", label: "MiB", factor: 1024 ** 2 },
  { value: "GiB", label: "GiB", factor: 1024 ** 3 },
];

/** Factors are in seconds. */
export const DURATION_UNITS: Unit[] = [
  { value: "s", label: "seconds", factor: 1 },
  { value: "m", label: "minutes", factor: 60 },
  { value: "h", label: "hours", factor: 3600 },
  { value: "d", label: "days", factor: 86400 },
];

const GO_UNIT_SECONDS: Record<string, number> = { ns: 1e-9, us: 1e-6, "µs": 1e-6, ms: 1e-3, s: 1, m: 60, h: 3600 };

/** Seconds in a Go duration text such as "1h30m", or null when it does not parse. */
export function goDurationSeconds(text: string): number | null {
  const re = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/gy;
  let total = 0;
  let end = 0;
  for (let m = re.exec(text); m; m = re.exec(text)) {
    total += parseFloat(m[1]) * GO_UNIT_SECONDS[m[2]];
    end = re.lastIndex;
  }
  return end === text.length && end > 0 ? total : null;
}

/** The largest unit that divides `amount` (in base units) into a whole number; else the largest unit of at least 1 (or the smallest unit), with decimals. */
function split(amount: number, units: Unit[]): Parts {
  for (let i = units.length - 1; i >= 0; i--) {
    const q = amount / units[i].factor;
    if (q >= 1 && Number.isInteger(q)) return { num: String(q), unit: units[i].value };
  }
  const u = [...units].reverse().find((x) => amount >= x.factor) ?? units[0];
  return { num: String(amount / u.factor), unit: u.value };
}

/** Number and unit of a size in bytes. */
export function splitSize(bytes: number): Parts {
  return split(bytes, SIZE_UNITS);
}

/** Number and unit of a Go duration text. */
export function splitDuration(text: string): Parts {
  const seconds = goDurationSeconds(text);
  return seconds === null ? { num: "", unit: "m" } : split(seconds, DURATION_UNITS);
}
