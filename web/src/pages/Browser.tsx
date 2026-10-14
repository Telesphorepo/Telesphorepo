import { useEffect, useState } from "react";
import { api, fmtSize, fmtTime, qs, useAsync, type Asset, type Page, type Repo, type Usage } from "../api";

function Copy({ text }: { text: string }) {
  const [done, setDone] = useState(false);
  const copy = async () => {
    try { await navigator.clipboard.writeText(text); setDone(true); setTimeout(() => setDone(false), 1500); } catch { /* clipboard unavailable */ }
  };
  return <button type="button" onClick={copy}>{done ? "Copied" : "Copy"}</button>;
}

function UsagePanel({ name }: { name: string }) {
  const { data, error } = useAsync(() => api<Usage>("GET", `/api/repos/${encodeURIComponent(name)}/usage`), [name]);
  return (
    <>
      {error && <p className="error">{error}</p>}
      {data?.snippets.length === 0 && <p className="muted">No usage notes for this format.</p>}
      {data?.snippets.map((s, i) => (
        <div key={i}>
          <div className="row"><strong className="grow">{s.title}</strong><Copy text={s.code} /></div>
          <pre>{s.code}</pre>
        </div>
      ))}
    </>
  );
}

function Status({ name }: { name: string }) {
  const { data } = useAsync(() => api<Record<string, unknown>>("GET", `/api/repos/${encodeURIComponent(name)}/status`), [name]);
  return (
    <>
      {Object.entries(data ?? {}).map(([k, v]) => (
        <span key={k} className="badge">{k}: {typeof v === "object" ? JSON.stringify(v) : String(v)}</span>
      ))}
    </>
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
      <h1>{name} {repo.data && <span className="badge">{repo.data.format}</span>} {repo.data?.anonymous_read && <span className="badge">anonymous</span>} <Status name={name} /></h1>
      {repo.error && <p className="error">{repo.error}</p>}
      {repo.data && <details><summary>Usage</summary><UsagePanel name={name} /></details>}
      <input className="wide" placeholder="Filter by path prefix" value={filter} onChange={(e) => setFilter(e.target.value)} />
      {err && <p className="error">{err}</p>}
      <table>
        <thead><tr><th>Path</th><th>Size</th><th>Type</th><th>Updated</th></tr></thead>
        <tbody>
          {items.map((a) => (
            <tr key={a.path}>
              <td className="mono">{repo.data ? <a href={`/${repo.data.format}/${name}/${a.path.split("/").map(encodeURIComponent).join("/")}`}>{a.path}</a> : a.path}</td>
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
