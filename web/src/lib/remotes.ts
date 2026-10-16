// The remote address of a proxy. The well-known remotes of each format are `remote_presets` of its
// descriptor (GET /api/formats).

/** The address as the server stores it: trimmed, with a "/" at the end. */
export const normalizeRemote = (url: string) => {
  const u = url.trim();
  return u && !u.endsWith("/") ? u + "/" : u;
};

/** An error for an address that is not http(s); "" when it is fine or empty. */
export function remoteError(url: string): string {
  const u = url.trim();
  if (!u) return "";
  try {
    const p = new URL(u);
    if (p.protocol === "http:" || p.protocol === "https:") return "";
  } catch {
    /* not a URL */
  }
  return "Enter an address that starts with https:// or http://.";
}
