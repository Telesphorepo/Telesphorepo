// The globs of the format descriptors (GET /api/formats), matched like the server matches them
// (internal/format Glob): "*" is any run of characters except "/", "?" one character except "/",
// and "**/" any number of folders, none included. Matching is case-sensitive.

const cache = new Map<string, RegExp>();

function regexOf(glob: string): RegExp {
  let re = cache.get(glob);
  if (re) return re;
  let s = "^";
  for (let i = 0; i < glob.length; ) {
    if (glob.startsWith("**/", i)) {
      s += "(?:[^/]*/)*";
      i += 3;
      continue;
    }
    const c = glob[i++];
    if (c === "*") s += "[^/]*";
    else if (c === "?") s += "[^/]";
    else s += c.replace(/[.+^${}()|[\]\\]/g, "\\$&");
  }
  re = new RegExp(s + "$");
  cache.set(glob, re);
  return re;
}

/** True when the repo path matches the glob. */
export const globMatch = (glob: string, path: string): boolean => regexOf(glob).test(path);

/** True when the path matches one of the globs. */
export const anyGlob = (globs: readonly string[], path: string): boolean => globs.some((g) => globMatch(g, path));
