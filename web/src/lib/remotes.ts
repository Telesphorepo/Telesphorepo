// Well-known remote repositories that a proxy can keep, per format: the quick choices beside the
// remote address on the New repository page and the Remote tab of a proxy.

export type RemotePreset = { label: string; url: string };

export const remotePresets: Record<string, RemotePreset[]> = {
  maven: [
    { label: "Maven Central", url: "https://repo1.maven.org/maven2/" },
    { label: "Gradle Plugin Portal", url: "https://plugins.gradle.org/m2/" },
  ],
  debian: [{ label: "Debian", url: "https://deb.debian.org/debian/" }],
};

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
