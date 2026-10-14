import { useEffect, useState } from "react";
import { api, fmtSize, fmtTime, qs, useAsync, type Asset, type Page, type Repo } from "../api";

function Usage({ repo }: { repo: Repo }) {
  const o = window.location.origin;
  const url = `${o}/${repo.format}/${repo.name}`;
  if (repo.format === "maven") {
    return (
      <pre>{`<!-- pom.xml -->
<repositories>
  <repository>
    <id>${repo.name}</id>
    <url>${url}</url>
  </repository>
</repositories>

<!-- ~/.m2/settings.xml -->
<servers>
  <server>
    <id>${repo.name}</id>
    <username>USER</username>
    <password>TOKEN</password>
  </server>
</servers>`}</pre>
    );
  }
  return (
    <pre>{`curl -fsSL ${o}/keys/debian.asc | sudo tee /etc/apt/keyrings/tr.asc >/dev/null
echo "deb [signed-by=/etc/apt/keyrings/tr.asc] ${url} DIST COMPONENT" | sudo tee /etc/apt/sources.list.d/${repo.name}.list`}</pre>
  );
}

export default function Browser({ name }: { name: string }) {
  const repo = useAsync(() => api<Repo>("GET", "/api/repos/" + encodeURIComponent(name)), [name]);
  const [filter, setFilter] = useState("");
  const [prefix, setPrefix] = useState("");
  const [items, setItems] = useState<Asset[]>([]);
  const [next, setNext] = useState<string | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const t = setTimeout(() => setPrefix(filter), 250);
    return () => clearTimeout(t);
  }, [filter]);

  const load = async (after?: string) => {
    setBusy(true); setErr("");
    try {
      const p = await api<Page<Asset>>("GET", `/api/repos/${encodeURIComponent(name)}/assets` + qs({ prefix, after, limit: "100" }));
      setItems((cur) => (after ? [...cur, ...p.items] : p.items));
      setNext(p.next);
    } catch (e) { setErr((e as Error).message); }
    setBusy(false);
  };
  useEffect(() => { setItems([]); setNext(null); load(); /* eslint-disable-next-line */ }, [name, prefix]);

  return (
    <>
      <h1>{name} {repo.data && <span className="badge">{repo.data.format}</span>} {repo.data?.anonymous_read && <span className="badge">anonymous</span>}</h1>
      {repo.error && <p className="error">{repo.error}</p>}
      {repo.data && <details><summary>Usage</summary><Usage repo={repo.data} /></details>}
      <input className="wide" placeholder="Filter by path prefix" value={filter} onChange={(e) => setFilter(e.target.value)} />
      {err && <p className="error">{err}</p>}
      <table>
        <thead><tr><th>Path</th><th>Size</th><th>Type</th><th>Updated</th></tr></thead>
        <tbody>
          {items.map((a) => (
            <tr key={a.path}>
              <td className="mono"><a href={`/${repo.data?.format ?? "maven"}/${name}/${a.path.split("/").map(encodeURIComponent).join("/")}`}>{a.path}</a></td>
              <td>{fmtSize(a.size)}</td><td>{a.kind}</td><td>{fmtTime(a.updated_at)}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {!busy && items.length === 0 && !err && <p className="muted">No assets.</p>}
      {next && <button disabled={busy} onClick={() => load(next)}>Load more</button>}
    </>
  );
}
