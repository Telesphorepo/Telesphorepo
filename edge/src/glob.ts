// An exact port of format.Glob (internal/format/ui.go): "*" matches any run of characters
// except "/", "?" one character except "/", "**/" any number of folders (none included), and a
// last segment "**" any rest of the path.
// Matching is case-sensitive. The pattern compiles to a regular expression as in Go; the "u" flag
// makes "[^/]" match one code point, as Go's regexp matches one rune.

const cache = new Map<string, RegExp>();

// The characters that regexp.QuoteMeta escapes.
const meta = new Set(["\\", ".", "+", "*", "?", "(", ")", "|", "[", "]", "{", "}", "^", "$"]);

function compile(pattern: string): RegExp {
  let re = cache.get(pattern);
  if (re) return re;
  let src = "^";
  const cps = Array.from(pattern);
  for (let i = 0; i < cps.length; ) {
    // A last segment "**" matches any rest of the path.
    if (cps[i] === "*" && cps[i + 1] === "*" && i + 2 === cps.length && (i === 0 || cps[i - 1] === "/")) {
      src += "[\\s\\S]+";
      break;
    }
    if (cps[i] === "*" && cps[i + 1] === "*" && cps[i + 2] === "/") {
      src += "(?:[^/]*/)*";
      i += 3;
      continue;
    }
    const c = cps[i];
    if (c === "*") src += "[^/]*";
    else if (c === "?") src += "[^/]";
    else if (meta.has(c)) src += "\\" + c;
    else if (c === "/") src += "\\/";
    else src += c;
    i++;
  }
  src += "$";
  re = new RegExp(src, "u");
  cache.set(pattern, re);
  return re;
}

// glob reports whether the repo path p matches pattern.
export function glob(pattern: string, p: string): boolean {
  return compile(pattern).test(p);
}

// immutable reports whether p matches an immutable glob and no mutable glob.
export function immutable(p: string, imm: readonly string[], mut: readonly string[]): boolean {
  return imm.some((g) => glob(g, p)) && !mut.some((g) => glob(g, p));
}

// immutableBy reports whether p is immutable under the rules of a repo object (format.ImmutableBy):
// the first rule whose glob matches decides; no match is mutable.
export function immutableBy(p: string, rules: readonly (readonly [string, boolean])[]): boolean {
  for (const [g, imm] of rules) if (glob(g, p)) return imm;
  return false;
}
