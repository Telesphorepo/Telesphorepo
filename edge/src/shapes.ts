// URL shapes: a request path to a repo and an asset path, as the origin's routes parse it. A
// path that the origin would redirect, refuse or route elsewhere gives null, and the request
// goes to the origin.
//
//   standard  /<format>/<repo>/<path>                     (format.Standard, httpx.RepoPath)
//   oci       /v2/<repo>/<image>/manifests/<tag|sha256:hex> and /v2/<repo>/<image>/blobs/sha256:<hex>
//             (internal/docker names.go: parseTarget, refPath)

import type { FormatConfig } from "./access";

export interface Target {
  format: FormatConfig;
  repo: string;
  path: string; // the asset path in the repo
}

// rawPath returns the path of the request URL as the client sent it, before any normalization.
export function rawPath(url: string): string {
  const scheme = url.indexOf("://");
  const start = url.indexOf("/", scheme < 0 ? 0 : scheme + 3);
  if (start < 0) return "/";
  let end = url.length;
  for (const c of ["?", "#"]) {
    const i = url.indexOf(c, start);
    if (i >= 0 && i < end) end = i;
  }
  return url.slice(start, end);
}

// unclean reports a path that Go's ServeMux redirects to its clean form (empty, "." or ".."
// segments, encoded or not), or that httpx.RepoPath refuses (an encoded "/", "\" or NUL).
function unclean(raw: string): boolean {
  const esc = raw.toLowerCase();
  if (esc.includes("%2f") || esc.includes("%5c") || esc.includes("%00")) return true;
  const segs = esc.split("/").slice(1);
  for (let i = 0; i < segs.length; i++) {
    const s = segs[i].replace(/%2e/g, ".");
    if (s === "." || s === "..") return true;
    if (s === "" && i < segs.length - 1) return true; // "//"
  }
  return false;
}

function decode(seg: string): string | null {
  try {
    return decodeURIComponent(seg);
  } catch {
    return null;
  }
}

const utf8 = new TextEncoder();

// validPath is repopath.Parse: valid UTF-8, no control character and no backslash, no empty, "."
// or ".." segment, at most 1024 bytes with at most 255 bytes per segment.
export function validPath(p: string): boolean {
  if (p === "" || utf8.encode(p).length > 1024) return false;
  for (const seg of p.split("/")) {
    if (seg === "" || seg === "." || seg === ".." || utf8.encode(seg).length > 255) return false;
    for (let i = 0; i < seg.length; i++) {
      const c = seg.charCodeAt(i);
      if (c < 0x20 || c === 0x7f || c === 0x5c) return false;
      // A lone surrogate is not valid UTF-8.
      if (c >= 0xd800 && c <= 0xdbff) {
        const d = seg.charCodeAt(i + 1);
        if (!(d >= 0xdc00 && d <= 0xdfff)) return false;
        i++;
      } else if (c >= 0xdc00 && c <= 0xdfff) {
        return false;
      }
    }
  }
  return true;
}

const segmentRE = /^[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*$/;
const tagRE = /^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$/;
const digestRE = /^[a-z0-9]+(?:[.+_-][a-z0-9]+)*:[a-zA-Z0-9=_-]+$/;
const hexRE = /^[a-f0-9]{64}$/;

// sha256Hex returns the hex of a "sha256:<hex>" digest, or null for any other reference.
function sha256Hex(ref: string): string | null {
  if (!digestRE.test(ref)) return null;
  const [alg, hex] = [ref.slice(0, ref.indexOf(":")), ref.slice(ref.indexOf(":") + 1)];
  return alg === "sha256" && hexRE.test(hex) ? hex : null;
}

function parseOCI(rest: string): { repo: string; path: string } | null {
  const raw = rest.split("/");
  const segs: string[] = [];
  for (const s of raw) {
    const d = decode(s);
    if (d === null) return null;
    segs.push(d);
  }
  const n = segs.length;
  // The order of parseTarget: uploads, then tags/list, then manifests and blobs (referrers and
  // every other action go to the origin).
  if (n >= 3 && segs[n - 1] === "" && segs[n - 2] === "uploads" && segs[n - 3] === "blobs") return null;
  if (n >= 3 && segs[n - 2] === "uploads" && segs[n - 3] === "blobs") return null;
  if (n >= 2 && segs[n - 1] === "list" && segs[n - 2] === "tags") return null;
  if (n < 2 || (segs[n - 2] !== "manifests" && segs[n - 2] !== "blobs")) return null;
  const action = segs[n - 2];
  const ref = segs[n - 1];
  const name = segs.slice(0, n - 2);
  if (name.length < 2 || name.join("/").length > 255 || !name.every((s) => segmentRE.test(s))) return null;
  const image = name.slice(1).join("/");
  let path: string;
  if (action === "blobs") {
    const hex = sha256Hex(ref);
    if (!hex) return null;
    path = image + "/_blobs/sha256/" + hex;
  } else if (ref.includes(":")) {
    const hex = sha256Hex(ref);
    if (!hex) return null;
    path = image + "/_manifests/sha256/" + hex;
  } else {
    if (!tagRE.test(ref)) return null;
    path = image + "/_tags/" + ref;
  }
  return { repo: name[0], path };
}

function parseStandard(rest: string): { repo: string; path: string } | null {
  const i = rest.indexOf("/");
  if (i <= 0) return null;
  const repo = decode(rest.slice(0, i));
  const parts: string[] = [];
  for (const s of rest.slice(i + 1).split("/")) {
    const d = decode(s);
    if (d === null) return null;
    parts.push(d);
  }
  const path = parts.join("/");
  if (!repo || !validPath(path)) return null;
  return { repo, path };
}

// parseTarget finds the format whose URL prefix the path has and parses the path with its shape.
export function parseTarget(url: string, formats: readonly FormatConfig[]): Target | null {
  const raw = rawPath(url);
  if (unclean(raw)) return null;
  for (const f of formats) {
    if (!raw.startsWith(f.prefix)) continue;
    const rest = raw.slice(f.prefix.length);
    const t = f.shape === "oci" ? parseOCI(rest) : parseStandard(rest); // pacman parses as standard
    return t ? { format: f, repo: t.repo, path: t.path } : null;
  }
  return null;
}

const pacmanArchRE = /^[a-z0-9_]{1,32}$/;
const pacmanNameRE = /^[a-z0-9@_+][a-z0-9@._+-]{0,127}$/;

// pacmanPath maps a path of a hosted repo of the pacman shape, reached through the repo name seg,
// to the path that holds its file, as internal/arch ServedPath does: <arch>/<seg>.db, .db.sig,
// .files and .files.sig are <arch>/index.db, ...; a package file
// <name>-<pkgver>-<pkgrel>-<parch>.pkg.tar.<ext>[.sig] of parch <arch> or any is
// pool/<name>/<file>. Every other path is itself.
export function pacmanPath(seg: string, path: string): string {
  const slash = path.indexOf("/");
  if (slash < 0) return path;
  const arch = path.slice(0, slash);
  const file = path.slice(slash + 1);
  if (file.includes("/") || !pacmanArchRE.test(arch) || arch === "pool") return path;
  for (const suffix of [".db", ".db.sig", ".files", ".files.sig"]) {
    if (file === seg + suffix) return arch + "/index" + suffix;
  }
  const base = file.endsWith(".sig") ? file.slice(0, -4) : file;
  const i = base.indexOf(".pkg.tar.");
  const ext = i > 0 ? base.slice(i + ".pkg.tar.".length) : "";
  if (i <= 0 || ext === "" || ext.includes(".") || ext.includes("/")) return path;
  const parts = base.slice(0, i).split("-");
  if (parts.length < 4) return path;
  const name = parts.slice(0, parts.length - 3).join("-");
  const parch = parts[parts.length - 1];
  if (!pacmanNameRE.test(name) || !pacmanArchRE.test(parch)) return path;
  return parch === arch || parch === "any" ? "pool/" + name + "/" + file : path;
}