import { useState } from "react";
import { api, useAsync, type Format, type Page, type Repo, type RepoConfig, type User } from "../api";
import { ConfigEditor } from "../formats";

type Perm = { username: string; level: "read" | "write" };

function Users() {
  const { data, error, reload } = useAsync(async () => {
    const all: User[] = [];
    let after: string | null = "";
    while (after !== null) {
      const p: Page<User> = await api<Page<User>>("GET", "/api/users?limit=200" + (after ? "&after=" + encodeURIComponent(after) : ""));
      all.push(...p.items);
      after = p.next;
    }
    return all;
  }, []);
  const [f, setF] = useState({ username: "", password: "", is_admin: false });
  const [err, setErr] = useState("");
  const act = async (fn: () => Promise<unknown>) => {
    setErr("");
    try { await fn(); reload(); } catch (e) { setErr((e as Error).message); }
  };
  const patch = (u: User, body: object) => act(() => api("PATCH", "/api/users/" + encodeURIComponent(u.username), body));
  return (
    <section>
      <h2>Users</h2>
      <form className="row" onSubmit={(e) => { e.preventDefault(); act(async () => { await api("POST", "/api/users", f); setF({ username: "", password: "", is_admin: false }); }); }}>
        <input placeholder="Username" required value={f.username} onChange={(e) => setF({ ...f, username: e.target.value })} />
        <input type="password" placeholder="Password" required value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} />
        <label className="inline"><input type="checkbox" checked={f.is_admin} onChange={(e) => setF({ ...f, is_admin: e.target.checked })} />admin</label>
        <button className="primary">Create user</button>
      </form>
      {(err || error) && <p className="error">{err || error}</p>}
      <table>
        <thead><tr><th>User</th><th>Type</th><th>Email</th><th>Admin</th><th>Disabled</th><th /></tr></thead>
        <tbody>
          {data?.map((u) => (
            <tr key={u.id}>
              <td>{u.username}</td>
              <td>{u.kind === "oidc" ? "sso" : "local"}</td>
              <td>{u.email || ""}</td>
              <td><input type="checkbox" checked={u.is_admin} onChange={(e) => patch(u, { is_admin: e.target.checked })} /></td>
              <td><input type="checkbox" checked={u.disabled} onChange={(e) => patch(u, { disabled: e.target.checked })} /></td>
              <td>{u.kind !== "oidc" && <button onClick={() => { const p = prompt(`New password for ${u.username}`); if (p) patch(u, { password: p }); }}>Reset password</button>}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}

function CreateRepo({ formats, onDone }: { formats: Format[]; onDone: () => void }) {
  const [f, setF] = useState({ name: "", format: "", anon: false });
  // undefined: the default config of the format; null: the editor holds an invalid config.
  const [config, setConfig] = useState<RepoConfig | null | undefined>(undefined);
  const [err, setErr] = useState("");
  const cur = formats.find((x) => x.name === f.format) ?? formats[0];
  const submit = async (e: React.FormEvent) => {
    e.preventDefault(); setErr("");
    try {
      await api("POST", "/api/repos", { name: f.name, format: cur.name, anonymous_read: f.anon, config: config ?? cur.default_config });
      setF({ ...f, name: "" }); onDone();
    } catch (e) { setErr((e as Error).message); }
  };
  if (!cur) return null;
  return (
    <section>
      <h2>Create repository</h2>
      <form className="row" onSubmit={submit}>
        <input placeholder="name" required pattern="[a-z0-9][a-z0-9._-]{0,63}" value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} />
        <select value={cur.name} onChange={(e) => { setF({ ...f, format: e.target.value }); setConfig(undefined); }}>
          {formats.map((x) => <option key={x.name} value={x.name}>{x.title}</option>)}
        </select>
        <ConfigEditor key={cur.name} format={cur.name} value={cur.default_config} onChange={setConfig} />
        <label className="inline"><input type="checkbox" checked={f.anon} onChange={(e) => setF({ ...f, anon: e.target.checked })} />anonymous read</label>
        <button className="primary" disabled={config === null}>Create</button>
      </form>
      {err && <p className="error">{err}</p>}
    </section>
  );
}

function RepoConfigForm({ repo, onSave }: { repo: Repo; onSave: (config: RepoConfig) => void }) {
  const [config, setConfig] = useState<RepoConfig | null>(repo.config ?? {});
  return (
    <details>
      <summary>Config</summary>
      <div className="row">
        <ConfigEditor format={repo.format} value={repo.config ?? {}} onChange={setConfig} />
        <button disabled={config === null} onClick={() => config && onSave(config)}>Save config</button>
      </div>
    </details>
  );
}

function Perms({ repo, onChange }: { repo: Repo; onChange: () => void }) {
  const n = encodeURIComponent(repo.name);
  const { data, error, reload } = useAsync(() => api<Perm[]>("GET", `/api/repos/${n}/permissions`), [repo.name]);
  const [user, setUser] = useState("");
  const [level, setLevel] = useState("read");
  const [err, setErr] = useState("");
  const act = async (fn: () => Promise<unknown>) => {
    setErr("");
    try { await fn(); reload(); onChange(); } catch (e) { setErr((e as Error).message); }
  };
  const set = (u: string, l: string) => act(() => api("PUT", `/api/repos/${n}/permissions/${encodeURIComponent(u)}`, { level: l }));
  return (
    <div className="card">
      <div className="row">
        <strong className="grow">{repo.name} <span className="badge">{repo.format}</span></strong>
        <label className="inline"><input type="checkbox" checked={repo.anonymous_read}
          onChange={(e) => act(() => api("PATCH", `/api/repos/${n}`, { anonymous_read: e.target.checked }))} />anonymous read</label>
      </div>
      {(err || error) && <p className="error">{err || error}</p>}
      <RepoConfigForm repo={repo} onSave={(config) => act(() => api("PATCH", `/api/repos/${n}`, { config }))} />
      <table>
        <tbody>
          {data?.map((p) => (
            <tr key={p.username}>
              <td>{p.username}</td>
              <td><select value={p.level} onChange={(e) => set(p.username, e.target.value)}><option>read</option><option>write</option></select></td>
              <td><button className="danger" onClick={() => act(() => api("DELETE", `/api/repos/${n}/permissions/${encodeURIComponent(p.username)}`))}>Remove</button></td>
            </tr>
          ))}
        </tbody>
      </table>
      <form className="row" onSubmit={(e) => { e.preventDefault(); set(user, level).then(() => setUser("")); }}>
        <input placeholder="Username" required value={user} onChange={(e) => setUser(e.target.value)} />
        <select value={level} onChange={(e) => setLevel(e.target.value)}><option>read</option><option>write</option></select>
        <button>Grant</button>
      </form>
    </div>
  );
}

export default function Admin() {
  const repos = useAsync(() => api<Repo[]>("GET", "/api/repos"), []);
  const formats = useAsync(() => api<Format[]>("GET", "/api/formats"), []);
  return (
    <>
      <h1>Admin</h1>
      <Users />
      {formats.error && <p className="error">{formats.error}</p>}
      <CreateRepo formats={formats.data ?? []} onDone={repos.reload} />
      <section>
        <h2>Repository permissions</h2>
        {repos.error && <p className="error">{repos.error}</p>}
        {repos.data?.map((r) => <Perms key={r.name} repo={r} onChange={repos.reload} />)}
      </section>
    </>
  );
}
