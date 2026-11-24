// Address ranges in the UI: the checks the server makes on IP rules and trusted proxies
// (internal/netrules), so a form can say what is wrong before it saves, and Find an address over
// the ranges of a network list that the page holds. The server checks again.

/** Whether s is an IPv4 address such as 203.0.113.7. */
function isIPv4(s: string): boolean {
  const parts = s.split(".");
  return parts.length === 4 && parts.every((p) => /^\d{1,3}$/.test(p) && Number(p) <= 255 && (p === "0" || !p.startsWith("0")));
}

/** An IPv4 address as a number, or null. */
function parseV4(s: string): bigint | null {
  if (!isIPv4(s)) return null;
  return s.split(".").reduce((n, p) => (n << 8n) | BigInt(p), 0n);
}

/** The 16-bit groups of one side of "::" (the last may be an IPv4 address), or null. */
function groups(part: string, last: boolean): number[] | null {
  if (part === "") return [];
  const out: number[] = [];
  const gs = part.split(":");
  for (let i = 0; i < gs.length; i++) {
    const g = gs[i];
    if (last && i === gs.length - 1 && g.includes(".")) {
      const v4 = parseV4(g);
      if (v4 === null) return null;
      out.push(Number(v4 >> 16n), Number(v4 & 0xffffn));
    } else if (/^[0-9a-fA-F]{1,4}$/.test(g)) {
      out.push(parseInt(g, 16));
    } else {
      return null;
    }
  }
  return out;
}

/** An IPv6 address such as 2001:db8::1 (no zone) as a number, or null. */
function parseV6(s: string): bigint | null {
  if (!s.includes(":")) return null;
  const halves = s.split("::");
  if (halves.length > 2) return null;
  const head = groups(halves[0], halves.length === 1);
  const tail = halves.length === 2 ? groups(halves[1], true) : [];
  if (!head || !tail) return null;
  const fill = 8 - head.length - tail.length;
  if (halves.length === 2 ? fill < 1 : fill !== 0) return null;
  return [...head, ...Array<number>(fill).fill(0), ...tail].reduce((n, w) => (n << 16n) | BigInt(w), 0n);
}

/**
 * Checks an address or a range: "203.0.113.7", "203.0.113.0/24", "2001:db8::/32". Returns ""
 * when it is valid, otherwise what is wrong, in words for the reader.
 */
export function checkRange(value: string): string {
  const v = value.trim();
  if (!v) return "Enter an address or a range, e.g. 203.0.113.0/24.";
  const [addr, bits, extra] = v.split("/");
  if (extra !== undefined) return `${v} has more than one "/".`;
  const v4 = isIPv4(addr);
  if (!v4 && parseV6(addr) === null) return `${addr} is not an IP address. Enter one such as 203.0.113.7 or 2001:db8::1.`;
  if (bits === undefined) return "";
  const max = v4 ? 32 : 128;
  if (!/^\d{1,3}$/.test(bits) || Number(bits) > max) return `The part after "/" must be a number from 0 to ${max}.`;
  return "";
}

/** How many addresses a range covers, in words: "1 address", "256 addresses", "a /8 range". */
export function rangeSize(value: string): string {
  const [addr, bits] = value.trim().split("/");
  const max = isIPv4(addr) ? 32 : 128;
  const free = max - (bits === undefined ? max : Number(bits));
  if (free === 0) return "1 address";
  if (free <= 24) return `${(2 ** free).toLocaleString()} addresses`;
  return `a /${max - free} range`;
}

/** The first and the last address of a range, in its family. */
type Span = { v6: boolean; lo: bigint; hi: bigint };

/**
 * An address or a range as its span, or null when it is neither. An IPv4 address written as IPv6
 * (::ffff:203.0.113.7) is the IPv4 address, as the server reads it.
 */
function spanOf(value: string): Span | null {
  const [addr, b, extra] = value.trim().split("/");
  if (extra !== undefined) return null;
  const v4 = parseV4(addr);
  let n = v4 ?? parseV6(addr);
  if (n === null) return null;
  let v6 = v4 === null;
  let max = v6 ? 128 : 32;
  if (b !== undefined && (!/^\d{1,3}$/.test(b) || Number(b) > max)) return null;
  let bits = b === undefined ? max : Number(b);
  if (v6 && n >> 32n === 0xffffn && bits >= 96) {
    [v6, n, max, bits] = [false, n & 0xffffffffn, 32, bits - 96];
  }
  const host = (1n << BigInt(max - bits)) - 1n;
  const lo = n & ~host;
  return { v6, lo, hi: lo | host };
}

/** What Find an address found: the ranges, and whether the text was an address, a range or other text. */
export type Found = { kind: "address" | "range" | "text"; ranges: string[] };

/**
 * Find an address over the ranges of one network list, which the page holds whole. An address
 * finds the ranges that hold it ("140.82.112.5" finds 140.82.112.0/20), a range the ranges that
 * overlap it, and other text the ranges whose text holds it ("10.1" finds 10.1.0.0/16 and
 * 10.10.0.0/16). Each range is read once, at the first address or range asked for.
 *
 *   const find = $derived(rangeFinder(ranges));
 *   const found = $derived(find(query)); // null for an empty query
 */
export function rangeFinder(ranges: readonly string[]): (query: string) => Found | null {
  let spans: (Span | null)[] | null = null;
  return (query) => {
    const q = query.trim();
    if (!q) return null;
    const s = spanOf(q);
    if (!s) {
      const low = q.toLowerCase();
      return { kind: "text", ranges: ranges.filter((r) => r.toLowerCase().includes(low)) };
    }
    const all = (spans ??= ranges.map(spanOf));
    return {
      kind: q.includes("/") ? "range" : "address",
      ranges: ranges.filter((_, i) => {
        const r = all[i];
        return r !== null && r.v6 === s.v6 && r.lo <= s.hi && s.lo <= r.hi;
      }),
    };
  };
}
