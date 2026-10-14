// Address ranges in the UI: the checks the server makes on IP rules and trusted proxies
// (internal/netrules), so a form can say what is wrong before it saves. The server checks again.

/** Whether s is an IPv4 address such as 203.0.113.7. */
function isIPv4(s: string): boolean {
  const parts = s.split(".");
  return parts.length === 4 && parts.every((p) => /^\d{1,3}$/.test(p) && Number(p) <= 255 && (p === "0" || !p.startsWith("0")));
}

/** Whether s is an IPv6 address such as 2001:db8::1 (no zone). The URL parser does the work. */
function isIPv6(s: string): boolean {
  if (!s.includes(":") || /[^0-9a-fA-F:.]/.test(s)) return false;
  try {
    new URL(`http://[${s}]/`);
    return true;
  } catch {
    return false;
  }
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
  if (!v4 && !isIPv6(addr)) return `${addr} is not an IP address. Enter one such as 203.0.113.7 or 2001:db8::1.`;
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
