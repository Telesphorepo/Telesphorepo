import { useState } from "react";
import { api, fmtTime, useAsync, type Page, type Repo, type Trust } from "../api";

type Row = { key: string; values: string };
type Form = { id: number; name: string; issuer: string; audience: string; claims: Row[]; jwks: string; repos: string[]; disabled: boolean };

const GITHUB_ISSUER = "https://token.actions.githubusercontent.com";

const presets: Record<string, { label: string; issuer: string; claims: Row[]; hint: string }> = {
  github: {
    label: "GitHub Actions",
    issuer: GITHUB_ISSUER,
    claims: [{ key: "repository", values: "" }, { key: "ref", values: "" }, { key: "workflow_ref", values: "" }],
    hint: "For example repository acme/app, ref refs/tags/v*, workflow_ref acme/app/.github/workflows/release.yml@*.",
  },
  jenkins: {
    label: "Jenkins",
    issuer: "https://JENKINS_URL/oidc",
    claims: [{ key: "sub", values: "" }],
    hint: "The issuer is the Jenkins URL followed by /oidc (OpenID Connect Provider plugin). Paste the JWKS if this server cannot reach Jenkins.",
  },
};

const blank = (): Form => ({ id: 0, name: "", issuer: "", audience: "", claims: [{ key: "", values: "" }], jwks: "", repos: [], disabled: false });

const toForm = (t: Trust): Form => ({
  id: t.id, name: t.name, issuer: t.issuer, audience: t.audience, jwks: t.jwks, repos: t.repos, disabled: t.disabled,
  claims: Object.entries(t.claims).map(([key, v]) => ({ key, values: v.join("\n") })),
});

const splitValues = (s: string) => s.split(/[\n,]/).map((x) => x.trim()).filter(Boolean);

function Editor({ initial, repos, onDone, onCancel }: { initial: Form; repos: Repo[]; onDone: () => void; onCancel: () => void }) {
  const [f, setF] = useState(initial);
  const [err, setErr] = useState("");
  const editing = f.id !== 0;

  const applyPreset = (k: string) => {
    const p = presets[k];
    setF({ ...f, issuer: p.issuer, claims: p.claims.map((c) => ({ ...c })) });
  };
  const setRow = (i: number, row: Row) => setF({ ...f, claims: f.claims.map((r, j) => (j === i ? row : r)) });
  const hint = Object.values(presets).find((p) => p.issuer === f.issuer)?.hint;

  const submit = async (e: React.FormEvent) => {
    e.preventDefault(); setErr("");
    const claims: Record<string, string[]> = {};
    for (const r of f.claims) {
      const v = splitValues(r.values);
      if (r.key.trim() && v.length) claims[r.key.trim()] = v;
    }
    if (Object.keys(claims).length === 0) { setErr("Add at least one claim with a value. Without a claim, every token of this issuer would match."); return; }
    if (f.repos.length === 0) { setErr("Select at least one repository."); return; }
    const body = { name: f.name, issuer: f.issuer.trim(), audience: f.audience.trim(), claims, jwks: f.jwks.trim(), repos: f.repos, disabled: f.disabled };
    try {
      if (editing) await api("PATCH", "/api/publish/trusts/" + f.id, body);
      else await api("POST", "/api/publish/trusts", body);
      onDone();
    } catch (e) { setErr((e as Error).message); }
  };

  return (
    <form className="card" onSubmit={submit}>
      <h2>{editing ? `Edit ${initial.name}` : "New trust policy"}</h2>
      {!editing && (
        <div className="row">
          <span className="muted">Preset:</span>
          {Object.entries(presets).map(([k, p]) => <button type="button" key={k} onClick={() => applyPreset(k)}>{p.label}</button>)}
        </div>
      )}
      <div className="stack">
        <label>Name<input required value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} /></label>
        <label>Issuer<input className="wide" required placeholder="https://token.actions.githubusercontent.com" value={f.issuer} onChange={(e) => setF({ ...f, issuer: e.target.value })} /></label>
        {hint && <p className="muted">{hint}</p>}
        <label>Audience<input className="wide" placeholder="Empty: the base URL of this server" value={f.audience} onChange={(e) => setF({ ...f, audience: e.target.value })} /></label>
      </div>
      <h3>Claims</h3>
      <p className="muted">Every claim listed must match. Give one value per line. A value is exact, or a pattern with <code>*</code>.</p>
      {f.claims.map((r, i) => (
        <div className="row top" key={i}>
          <input placeholder="Claim, for example repository" value={r.key} onChange={(e) => setRow(i, { ...r, key: e.target.value })} />
          <textarea rows={2} placeholder="Allowed values" value={r.values} onChange={(e) => setRow(i, { ...r, values: e.target.value })} />
          <button type="button" onClick={() => setF({ ...f, claims: f.claims.filter((_, j) => j !== i) })}>Remove</button>
        </div>
      ))}
      <button type="button" onClick={() => setF({ ...f, claims: [...f.claims, { key: "", values: "" }] })}>Add claim</button>
      <h3>Repositories</h3>
      <p className="muted">A session from this policy can write to the selected repositories only.</p>
      <select multiple size={Math.min(8, Math.max(3, repos.length))} value={f.repos}
        onChange={(e) => setF({ ...f, repos: Array.from(e.target.selectedOptions, (o) => o.value) })}>
        {repos.map((r) => <option key={r.name} value={r.name}>{r.name} ({r.format})</option>)}
      </select>
      <details open={!!f.jwks}>
        <summary>Pasted JWKS (optional)</summary>
        <p className="muted">When set, the server uses these keys instead of fetching them from the issuer.</p>
        <textarea className="wide mono" rows={5} value={f.jwks} onChange={(e) => setF({ ...f, jwks: e.target.value })} />
      </details>
      <label className="inline"><input type="checkbox" checked={f.disabled} onChange={(e) => setF({ ...f, disabled: e.target.checked })} />disabled</label>
      {err && <p className="error">{err}</p>}
      <div className="row"><button className="primary">{editing ? "Save" : "Create"}</button><button type="button" onClick={onCancel}>Cancel</button></div>
    </form>
  );
}

export default function Trusts() {
  const repos = useAsync(() => api<Repo[]>("GET", "/api/repos"), []);
  const list = useAsync(async () => {
    const all: Trust[] = [];
    let after: string | null = "";
    while (after !== null) {
      const p: Page<Trust> = await api<Page<Trust>>("GET", "/api/publish/trusts?limit=200" + (after ? "&after=" + encodeURIComponent(after) : ""));
      all.push(...p.items);
      after = p.next;
    }
    return all;
  }, []);
  const [form, setForm] = useState<Form>();
  const [err, setErr] = useState("");
  const act = async (fn: () => Promise<unknown>) => {
    setErr("");
    try { await fn(); list.reload(); } catch (e) { setErr((e as Error).message); }
  };
  const toggle = (t: Trust) => act(() => api("PATCH", "/api/publish/trusts/" + t.id, {
    name: t.name, issuer: t.issuer, audience: t.audience, claims: t.claims, jwks: t.jwks, repos: t.repos, disabled: !t.disabled,
  }));
  const remove = (t: Trust) => confirm(`Delete the trust policy ${t.name}? Its open sessions end.`) && act(() => api("DELETE", "/api/publish/trusts/" + t.id));

  return (
    <>
      <h1>Trust policies</h1>
      <p className="muted">
        A trust policy lets a CI system publish without a stored secret. The CI job sends its signed identity token,
        and the server creates a short publish session when the token matches a policy.
      </p>
      {(err || list.error || repos.error) && <p className="error">{err || list.error || repos.error}</p>}
      {form ? (
        <Editor initial={form} repos={repos.data ?? []} onCancel={() => setForm(undefined)} onDone={() => { setForm(undefined); list.reload(); }} />
      ) : (
        <button className="primary" onClick={() => setForm(blank())}>New trust policy</button>
      )}
      <table>
        <thead><tr><th>Name</th><th>Issuer</th><th>Claims</th><th>Repositories</th><th>Created</th><th /></tr></thead>
        <tbody>
          {list.data?.map((t) => (
            <tr key={t.id}>
              <td>{t.name}{t.disabled && <> <span className="badge">disabled</span></>}</td>
              <td className="mono">{t.issuer}</td>
              <td className="mono">{Object.entries(t.claims).map(([k, v]) => <div key={k}>{k}: {v.join(", ")}</div>)}</td>
              <td>{t.repos.join(", ")}</td>
              <td>{fmtTime(t.created_at)}</td>
              <td className="actions">
                <button onClick={() => setForm(toForm(t))}>Edit</button>
                <button onClick={() => toggle(t)}>{t.disabled ? "Enable" : "Disable"}</button>
                <button className="danger" onClick={() => remove(t)}>Delete</button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {list.data?.length === 0 && <p className="muted">No trust policies.</p>}
    </>
  );
}
