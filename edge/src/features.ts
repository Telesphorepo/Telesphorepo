// The edge contract (SPEC.md "Edge delivery", The contract): the Worker serves by the features it
// implements, never by the server version. config.json names in `requires` the features that the
// server uses now, and an object (an access object, a format of config.json) names in `needs` those
// that reading it takes. A Worker that lacks one passes to the server: every request for
// `requires`, the requests that read the object for `needs`. The list only grows.

export const FEATURES: readonly string[] = [];

const known = new Set(FEATURES);

// knows reports whether the Worker implements every feature that v (a `requires` or `needs` value)
// names. No value names none; a value that is not a list of names is one the Worker cannot read.
export function knows(v: unknown): boolean {
  if (v === undefined || v === null) return true;
  return Array.isArray(v) && v.every((f) => typeof f === "string" && known.has(f));
}
