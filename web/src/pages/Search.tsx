import { useState } from "react";
import { api, fmtSize, qs, type Asset, type Page } from "../api";

type Hit = Asset & { repo?: string; repo_name?: string };

export default function Search() {
  const [q, setQ] = useState("");
  const [items, setItems] = useState<Hit[]>([]);
  const [next, setNext] = useState<string | null>(null);
  const [err, setErr] = useState("");
  const [done, setDone] = useState(false);

  const run = async (after?: string) => {
    setErr("");
    try {
      const p = await api<Page<Hit>>("GET", "/api/search" + qs({ q, after, limit: "50" }));
      setItems((cur) => (after ? [...cur, ...p.items] : p.items));
      setNext(p.next); setDone(true);
    } catch (e) { setErr((e as Error).message); }
  };
  return (
    <>
      <h1>Search</h1>
      <form className="row" onSubmit={(e) => { e.preventDefault(); if (q.length >= 3) run(); }}>
        <input className="wide" placeholder="At least 3 characters" value={q} onChange={(e) => setQ(e.target.value)} />
        <button className="primary" disabled={q.length < 3}>Search</button>
      </form>
      {err && <p className="error">{err}</p>}
      {items.length > 0 && (
        <table>
          <thead><tr><th>Repository</th><th>Path</th><th>Size</th></tr></thead>
          <tbody>
            {items.map((a, i) => {
              const r = a.repo ?? a.repo_name ?? "";
              return (
                <tr key={r + a.path + i}>
                  <td><a href={"#/repo/" + encodeURIComponent(r)}>{r}</a></td>
                  <td className="mono">{a.path}</td><td>{fmtSize(a.size)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
      {done && items.length === 0 && <p className="muted">No results.</p>}
      {next && <button onClick={() => run(next)}>Load more</button>}
    </>
  );
}
