import { useState } from "react";
import { api, ApiError, useAsync } from "../api";

type Val = string | number | boolean | string[] | { set: boolean } | null;
type Setting = { key: string; label: string; help?: string; type: "string" | "bool" | "int" | "duration" | "list"; options?: string[]; value: Val; secret: boolean; source: "db" | "env" | "default"; locked: boolean; read_only?: boolean };
type Group = { name: string; label?: string; settings: Setting[] };
type KeyInfo = { fingerprint?: string; public_key?: string };

const show = (s: Setting) => (Array.isArray(s.value) ? s.value.join(s.type === "list" ? "\n" : ", ") : String(s.value ?? ""));
const splitList = (v: string) => v.split(/[,\n]/).map((x) => x.trim()).filter(Boolean);
const parse = (s: Setting, v: string): Val =>
  s.type === "int" ? (v === "" ? null : Number(v)) : v;

function Field({ s, draft, error, replacing, set }: { s: Setting; draft: Val | undefined; error?: string; replacing: boolean; set: (v: Val | undefined) => void }) {
  const ro = s.locked || s.read_only;
  const cur = draft === undefined ? (s.secret ? "" : s.type === "bool" ? s.value : show(s)) : draft;
  let input;
  if (s.secret && !replacing && !ro) {
    input = <><span>{(s.value as { set: boolean })?.set ? "••• set" : "not set"}</span><button type="button" onClick={() => set("")}>Replace</button></>;
  } else if (s.secret && ro) {
    input = <span>{(s.value as { set: boolean })?.set ? "••• set" : "not set"}</span>;
  } else if (s.type === "bool") {
    input = <input type="checkbox" disabled={ro} checked={!!cur} onChange={(e) => set(e.target.checked)} />;
  } else if (s.type === "list") {
    input = <textarea className="grow" rows={Math.min(6, Math.max(2, String(cur ?? "").split("\n").length))} disabled={ro} autoComplete="off"
      placeholder="comma or newline separated" value={String(cur ?? "")} onChange={(e) => set(e.target.value)} />;
  } else if (s.options?.length) {
    input = <select disabled={ro} value={String(cur)} onChange={(e) => set(e.target.value)}>{s.options.map((o) => <option key={o}>{o}</option>)}</select>;
  } else {
    input = <input className="grow" disabled={ro} type={s.secret ? "password" : s.type === "int" ? "number" : "text"} autoComplete="off"
      placeholder={s.secret ? "new value (empty resets)" : undefined} value={String(cur ?? "")}
      onChange={(e) => set(s.secret ? e.target.value : parse(s, e.target.value))} />;
  }
  return (
    <div className="field">
      <div className="row">
        <strong>{s.label}</strong>
        {s.locked && <span className="badge" title="Change the environment variable to modify this">set by environment</span>}
        {input}
      </div>
      {s.help && <div className="muted">{s.help}</div>}
      {error && <p className="error">{error}</p>}
    </div>
  );
}

function Section({ g, reload, children }: { g: Group; reload: () => void; children?: (patch: () => Record<string, Val>) => React.ReactNode }) {
  const [draft, setDraft] = useState<Record<string, Val>>({});
  const [errs, setErrs] = useState<Record<string, string>>({});
  const [msg, setMsg] = useState("");
  const [err, setErr] = useState("");
  const patch = () => {
    const p: Record<string, Val> = {};
    for (const s of g.settings) if (s.key in draft && !s.locked) {
      const d = draft[s.key];
      p[s.key] = s.secret && d === "" ? null : s.type === "list" && typeof d === "string" ? splitList(d) : d;
    }
    return p;
  };
  const dirty = Object.keys(patch()).length > 0;
  const save = async () => {
    setErrs({}); setErr(""); setMsg("");
    try {
      await api("PATCH", "/api/settings", patch());
      setDraft({}); setMsg("Saved."); reload();
    } catch (e) {
      const f = e instanceof ApiError ? e.fields : undefined;
      setErrs(f ?? {});
      setErr(f ? "" : (e as Error).message);
    }
  };
  return (
    <section>
      <h2>{g.label || g.name[0].toUpperCase() + g.name.slice(1)}</h2>
      <div className="card">
        {g.settings.map((s) => (
          <Field key={s.key} s={s} draft={draft[s.key]} error={errs[s.key]} replacing={s.key in draft}
            set={(v) => setDraft((d) => { const n = { ...d }; if (v === undefined) delete n[s.key]; else n[s.key] = v; return n; })} />
        ))}
        {children?.(patch)}
        <div className="row">
          <button className="primary" disabled={!dirty} onClick={save}>Save {g.label || g.name}</button>
          {msg && <span className="muted">{msg}</span>}
          {err && <span className="error">{err}</span>}
        </div>
      </div>
    </section>
  );
}

// redirectURL is the OIDC callback URL: oidc.redirect_url, else base_url + the callback path,
// else the browser origin + the callback path.
function redirectURL(groups: Group[], draft: Record<string, Val>): string {
  const path = "/api/auth/oidc/callback";
  const all = groups.flatMap((g) => g.settings);
  const val = (k: string) => String((k in draft ? draft[k] : all.find((s) => s.key === k)?.value) ?? "").trim();
  return val("oidc.redirect_url") || (val("base_url") ? val("base_url").replace(/\/+$/, "") + path : window.location.origin + path);
}

function SSOExtras({ patch, groups }: { patch: () => Record<string, Val>; groups: Group[] }) {
  const [res, setRes] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const test = async () => {
    setBusy(true); setRes(null);
    try { await api("POST", "/api/settings/oidc/test", patch()); setRes({ ok: true, text: "Discovery OK." }); }
    catch (e) {
      const f = e instanceof ApiError ? e.fields : undefined;
      setRes({ ok: false, text: f ? Object.values(f).join("; ") : (e as Error).message });
    }
    setBusy(false);
  };
  return (
    <>
      <p className="muted">Register this redirect URL at your IdP: <code>{redirectURL(groups, patch())}</code></p>
      <div className="row">
        <button disabled={busy} onClick={test}>Test discovery</button>
        {res && <span className={res.ok ? "muted" : "error"}>{res.text}</span>}
      </div>
    </>
  );
}

function StorageExtras({ settings, patch }: { settings: Setting[]; patch: () => Record<string, Val> }) {
  const [res, setRes] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const switching = settings.some((s) => s.key === "storage") && "storage" in patch();
  const test = async () => {
    setBusy(true); setRes(null);
    try { await api("POST", "/api/settings/storage/test", patch()); setRes({ ok: true, text: "Connection OK." }); }
    catch (e) { setRes({ ok: false, text: (e as Error).message }); }
    setBusy(false);
  };
  return (
    <>
      {switching && <p className="error">Switching the storage backend does not move existing blobs; they stay in the old backend.</p>}
      <div className="row">
        <button disabled={busy} onClick={test}>Test connection</button>
        {res && <span className={res.ok ? "muted" : "error"}>{res.text}</span>}
      </div>
    </>
  );
}

function SigningKey() {
  const { data, error, reload } = useAsync(() => api<KeyInfo & { from_file?: boolean }>("GET", "/api/settings/signing-key"), []);
  const [upload, setUpload] = useState<string | null>(null);
  const [err, setErr] = useState("");
  const post = async (armored: string) => {
    setErr("");
    try { await api("POST", "/api/settings/signing-key", { armored }); setUpload(null); reload(); }
    catch (e) { setErr((e as Error).message); }
  };
  return (
    <div className="card">
      <strong>Signing key</strong>
      <p>Fingerprint: <code>{data?.fingerprint || "none"}</code> · {data?.public_key && <a href={"data:text/plain;charset=utf-8," + encodeURIComponent(data.public_key)} download="signing-key.asc">Download public key</a>}</p>
      {(err || error) && <p className="error">{err || error}</p>}
      {upload === null ? (
        <div className="row">
          <button className="danger" onClick={() => confirm("Generate a new signing key? Clients must trust the new public key, and the old key is discarded.") && post("")}>Generate new key</button>
          <button onClick={() => setUpload("")}>Upload key</button>
        </div>
      ) : (
        <>
          <textarea rows={8} style={{ width: "100%" }} placeholder="-----BEGIN PGP PRIVATE KEY BLOCK-----" value={upload} onChange={(e) => setUpload(e.target.value)} />
          <div className="row">
            <button className="primary" disabled={!upload.trim()} onClick={() => post(upload)}>Save key</button>
            <button onClick={() => setUpload(null)}>Cancel</button>
          </div>
        </>
      )}
    </div>
  );
}

export default function Settings() {
  const { data, error, reload } = useAsync(() => api<{ groups: Group[] } | Setting[]>("GET", "/api/settings"), []);
  // Accept either {groups:[...]} or a flat item list carrying `group`.
  const groups: Group[] = [];
  if (Array.isArray(data)) for (const i of data as (Setting & { group: string })[]) {
    const g = groups.find((x) => x.name === i.group) ?? (groups.push({ name: i.group, settings: [] }), groups[groups.length - 1]);
    g.settings.push(i);
  } else if (data) groups.push(...data.groups);
  return (
    <>
      <h1>Settings</h1>
      {error && <p className="error">{error}</p>}
      {groups.map((g) => (
        <Section key={g.name} g={g} reload={reload}>
          {g.name.toLowerCase() === "storage" ? (patch) => <StorageExtras settings={g.settings} patch={patch} /> :
            g.name.toLowerCase() === "signing" ? () => <SigningKey /> :
            g.name.toLowerCase() === "sso" ? (patch) => <SSOExtras patch={patch} groups={groups} /> : undefined}
        </Section>
      ))}
    </>
  );
}
