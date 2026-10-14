import { useState } from "react";
import { api, fmtTime, useAsync, type Token } from "../api";

export default function Tokens() {
  const { data, error, reload } = useAsync(() => api<Token[]>("GET", "/api/tokens"), []);
  const [name, setName] = useState("");
  const [days, setDays] = useState("");
  const [created, setCreated] = useState<string>();
  const [err, setErr] = useState("");
  const [copied, setCopied] = useState(false);

  const create = async (e: React.FormEvent) => {
    e.preventDefault(); setErr("");
    try {
      const t = await api<Token>("POST", "/api/tokens", { name, ...(days ? { expires_in_days: Number(days) } : {}) });
      setCreated(t.token); setName(""); setDays(""); setCopied(false); reload();
    } catch (e) { setErr((e as Error).message); }
  };
  const revoke = async (id: string) => {
    if (!confirm("Revoke this token?")) return;
    try { await api("DELETE", "/api/tokens/" + id); reload(); } catch (e) { setErr((e as Error).message); }
  };
  const copy = async () => { await navigator.clipboard.writeText(created!); setCopied(true); };

  return (
    <>
      <h1>My tokens</h1>
      {created && (
        <div className="card notice">
          <p>Copy your new token now. It will not be shown again.</p>
          <div className="row"><code className="mono grow">{created}</code><button onClick={copy}>{copied ? "Copied" : "Copy"}</button><button onClick={() => setCreated(undefined)}>Dismiss</button></div>
        </div>
      )}
      <form className="row" onSubmit={create}>
        <input placeholder="Token name" required value={name} onChange={(e) => setName(e.target.value)} />
        <input type="number" min="1" placeholder="Expires in days (optional)" value={days} onChange={(e) => setDays(e.target.value)} />
        <button className="primary">Create</button>
      </form>
      {(err || error) && <p className="error">{err || error}</p>}
      <table>
        <thead><tr><th>Name</th><th>Created</th><th>Expires</th><th>Last used</th><th /></tr></thead>
        <tbody>
          {data?.map((t) => (
            <tr key={t.id}>
              <td>{t.name}</td><td>{fmtTime(t.created_at)}</td>
              <td>{t.expires_at ? fmtTime(t.expires_at) : "never"}</td><td>{fmtTime(t.last_used_at)}</td>
              <td><button className="danger" onClick={() => revoke(t.id)}>Revoke</button></td>
            </tr>
          ))}
        </tbody>
      </table>
      {data?.length === 0 && <p className="muted">No tokens.</p>}
    </>
  );
}
