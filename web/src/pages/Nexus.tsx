import { useCallback, useEffect, useState } from "react";
import {
  addNexusSource, cancelNexusSync, cutOverNexus, deleteNexusSource, fmtTime, getNexusSource, getNexusSync,
  go, listNexusSources, nexusSyncFailures, nexusSyncs, saveNexusRepos, syncNexusNow, updateNexusCredentials, useAsync,
  type NexusFailure, type NexusSourceDetail, type NexusSync,
} from "../api";

const stateLabel = (s: string) => (s === "cut_over" ? "cut over" : s);

// Nexus is the admin page for Nexus sources. Passwords live only in form state
// and are cleared after each request.
export default function Nexus({ id }: { id: number }) {
  return id > 0 ? <SourceView id={id} /> : <SourceList />;
}

function SourceList() {
  const { data, error, reload } = useAsync(listNexusSources, []);
  const [name, setName] = useState("");
  const [baseURL, setBaseURL] = useState("");
  const [user, setUser] = useState("");
  const [pass, setPass] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setErr("");
    try {
      const s = await addNexusSource(name.trim(), baseURL.trim(), user, pass);
      setPass("");
      reload();
      go("/nexus/" + s.id);
    } catch (x) { setErr((x as Error).message); }
    setPass(""); setBusy(false);
  };

  return (
    <>
      <h1>Nexus sources</h1>
      {error && <p className="error">{error}</p>}
      {data && data.length === 0 && <p className="muted">No Nexus source yet.</p>}
      {data && data.length > 0 && (
        <table>
          <thead><tr><th>Name</th><th>Base URL</th><th>State</th><th>Last sync</th></tr></thead>
          <tbody>
            {data.map((s) => (
              <tr key={s.id} className="click" onClick={() => go("/nexus/" + s.id)}>
                <td><a href={"#/nexus/" + s.id}>{s.name}</a></td>
                <td><code>{s.base_url}</code></td>
                <td><span className="badge">{stateLabel(s.state)}</span></td>
                <td>{fmtTime(s.last_sync_at)}{s.last_error && <div className="error">{s.last_error}</div>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <form className="card" onSubmit={add} autoComplete="off">
        <h2>Add a source</h2>
        {err && <p className="error">{err}</p>}
        <div className="row"><label>Name <input required value={name} onChange={(e) => setName(e.target.value)} /></label></div>
        <div className="row"><label>Base URL <input className="grow" type="url" required placeholder="https://nexus.example.com" value={baseURL} onChange={(e) => setBaseURL(e.target.value)} /></label></div>
        <div className="row"><label>Username <input required autoComplete="off" value={user} onChange={(e) => setUser(e.target.value)} /></label></div>
        <div className="row"><label>Password <input required type="password" autoComplete="off" value={pass} onChange={(e) => setPass(e.target.value)} /></label></div>
        <div className="row"><button className="primary" disabled={busy}>Add source</button></div>
      </form>
    </>
  );
}

type Pick = { on: boolean; target: string; locked: boolean };

function SourceView({ id }: { id: number }) {
  const { data, error, reload } = useAsync(() => getNexusSource(id), [id]);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [shown, setShown] = useState<number | null>(null);
  const [history, setHistory] = useState<NexusSync[]>([]);
  const [next, setNext] = useState<string | null>(null);
  const [confirmCut, setConfirmCut] = useState(false);
  const [lastFailed, setLastFailed] = useState(0);

  const loadHistory = useCallback(async (after?: string) => {
    try {
      const p = await nexusSyncs(id, after);
      setHistory((h) => (after ? [...h, ...p.items] : p.items));
      setNext(p.next);
      if (!after) setShown((cur) => cur ?? p.items.find((s) => s.state === "running")?.id ?? null);
    } catch (e) { setErr((e as Error).message); }
  }, [id]);
  useEffect(() => { setHistory([]); setNext(null); setShown(null); loadHistory(); }, [loadHistory]);

  const run = async (fn: () => Promise<unknown>) => {
    setBusy(true); setErr("");
    try { await fn(); } catch (e) { setErr((e as Error).message); }
    setBusy(false);
  };

  const syncNow = () => run(async () => {
    const s = await syncNexusNow(id);
    setShown(s.id);
    await loadHistory();
  });
  const openCutOver = () => run(async () => {
    const p = await nexusSyncs(id);
    setLastFailed(p.items[0]?.failed ?? 0);
    setConfirmCut(true);
  });
  const cutOver = () => run(async () => {
    await cutOverNexus(id);
    setConfirmCut(false);
    reload();
    await loadHistory();
  });
  const remove = () => {
    if (!confirm("Delete this Nexus source? Mirrored repositories and their files stay.")) return;
    run(async () => { await deleteNexusSource(id); go("/nexus"); });
  };
  const finished = useCallback(() => { reload(); loadHistory(); }, [reload, loadHistory]);

  if (error) return <p className="error">{error}</p>;
  if (!data) return null;
  const mirroring = data.state === "mirroring";
  const running = history.some((s) => s.state === "running");

  return (
    <>
      <p><a href="#/nexus">All sources</a></p>
      <h1>{data.name} <span className="badge">{stateLabel(data.state)}</span></h1>
      <div className="card">
        <div className="row"><code>{data.base_url}</code><span className="muted">Last sync: {fmtTime(data.last_sync_at)}</span></div>
        {data.last_error && <p className="error">{data.last_error}</p>}
        {err && <p className="error">{err}</p>}
        {!mirroring && data.cutover_failed > 0 && (
          <p className="error">{data.cutover_failed} files failed to import in the cut-over sync and are not on this
            server. <a href="#" onClick={(e) => { e.preventDefault(); setShown(data.cutover_sync_id); }}>Show the failed files</a></p>
        )}
        {mirroring ? (
          <div className="row">
            <button className="primary" disabled={busy || running} onClick={syncNow}>Sync now</button>
            <button className="danger" disabled={busy || running} onClick={openCutOver}>Cut over</button>
          </div>
        ) : (
          <div className="row">
            <span className="muted">Uploads go to this server. The source credentials are deleted.</span>
            <button className="danger" disabled={busy} onClick={remove}>Delete source</button>
          </div>
        )}
        {confirmCut && mirroring && (
          <div className="card notice">
            <strong>Cut over to this server</strong>
            <p>The server runs a final sync. When it reaches the end of every repository, the mirrored repositories accept uploads here, the
              server stops syncing from Nexus and it deletes the stored Nexus credentials. Files deleted in Nexus are not
              deleted here. Make sure that every client uploads to this server before you continue.</p>
            {lastFailed > 0 && (
              <p className="error">{lastFailed} files failed to import in the last sync; they will not be on this
                server after cut-over.</p>
            )}
            <div className="row">
              <button className="danger" disabled={busy} onClick={cutOver}>Run final sync and cut over</button>
              <button disabled={busy} onClick={() => setConfirmCut(false)}>Cancel</button>
            </div>
          </div>
        )}
      </div>

      {mirroring && <Credentials id={id} onSaved={reload} />}
      <RepoMapping key={data.repos.map((r) => r.source_repo + ":" + (r.target ?? "")).join("|")} source={data} onSaved={reload} />

      <h2>Syncs</h2>
      {history.length === 0 && <p className="muted">No sync yet.</p>}
      {history.length > 0 && (
        <table>
          <thead><tr><th>Started</th><th>Finished</th><th>State</th><th></th></tr></thead>
          <tbody>
            {history.map((s) => (
              <tr key={s.id}>
                <td>{fmtTime(s.started_at)}</td>
                <td>{fmtTime(s.finished_at)}</td>
                <td>{s.state}{s.error && <div className="error">{s.error}</div>}</td>
                <td><button onClick={() => setShown(s.id)}>Details</button></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {next && <div className="row"><button onClick={() => loadHistory(next)}>Load more</button></div>}
      {shown !== null && <SyncDetail key={shown} id={shown} onFinished={finished} />}
    </>
  );
}

function Credentials({ id, onSaved }: { id: number; onSaved: () => void }) {
  const [user, setUser] = useState("");
  const [pass, setPass] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [ok, setOk] = useState(false);

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setErr(""); setOk(false);
    try { await updateNexusCredentials(id, user, pass); setOk(true); setUser(""); onSaved(); }
    catch (x) { setErr((x as Error).message); }
    setPass(""); setBusy(false);
  };
  return (
    <details>
      <summary>Update credentials</summary>
      <form className="card" onSubmit={save} autoComplete="off">
        {err && <p className="error">{err}</p>}
        {ok && <p className="muted">Credentials saved.</p>}
        <div className="row">
          <label>Username <input required autoComplete="off" value={user} onChange={(e) => setUser(e.target.value)} /></label>
          <label>Password <input required type="password" autoComplete="off" value={pass} onChange={(e) => setPass(e.target.value)} /></label>
          <button className="primary" disabled={busy}>Save</button>
        </div>
      </form>
    </details>
  );
}

function RepoMapping({ source, onSaved }: { source: NexusSourceDetail; onSaved: () => void }) {
  const mirroring = source.state === "mirroring";
  const [picks, setPicks] = useState<Record<string, Pick>>(() =>
    Object.fromEntries(source.repos.map((r) => [r.source_repo, r.target
      ? { on: true, target: r.target, locked: true }
      : { on: false, target: r.source_repo, locked: false }])));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setErr("");
    try {
      const maps = source.repos
        .filter((r) => picks[r.source_repo]?.on)
        .map((r) => ({ source_repo: r.source_repo, target: picks[r.source_repo].target.trim(), create: !picks[r.source_repo].locked }));
      await saveNexusRepos(source.id, maps);
      onSaved();
    } catch (x) { setErr((x as Error).message); }
    setBusy(false);
  };
  const dirty = source.repos.some((r) => picks[r.source_repo]?.on && !picks[r.source_repo].locked);

  return (
    <form onSubmit={save} autoComplete="off">
      <h2>Nexus repositories</h2>
      {err && <p className="error">{err}</p>}
      {source.repos.length === 0 && <p className="muted">Nexus lists no repositories.</p>}
      {source.repos.length > 0 && (
        <table>
          <thead><tr><th></th><th>Nexus repository</th><th>Format</th><th>Target name</th></tr></thead>
          <tbody>
            {source.repos.map((r) => {
              const p = picks[r.source_repo];
              const set = (v: Partial<Pick>) => setPicks((s) => ({ ...s, [r.source_repo]: { ...s[r.source_repo], ...v } }));
              const editable = mirroring && r.supported && !p?.locked;
              return (
                <tr key={r.source_repo} className={r.supported ? "" : "muted"}>
                  <td><input type="checkbox" disabled={!editable} checked={!!p?.on} onChange={(e) => set({ on: e.target.checked })} /></td>
                  <td>{r.source_repo}</td>
                  <td>{r.source_format}{!r.supported && <> <span className="badge">no plugin for format {r.source_format}</span></>}</td>
                  <td><input disabled={!editable || !p?.on} required={p?.on} value={p?.target ?? ""} onChange={(e) => set({ target: e.target.value })} /></td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
      {mirroring && (
        <div className="row">
          <button className="primary" disabled={busy || !dirty}>Save mapping</button>
          <span className="muted">A new target repository is read-only for uploads while it mirrors Nexus.</span>
        </div>
      )}
    </form>
  );
}

function SyncDetail({ id, onFinished }: { id: number; onFinished: () => void }) {
  const [sync, setSync] = useState<NexusSync | null>(null);
  const [err, setErr] = useState("");
  const [fails, setFails] = useState<NexusFailure[]>([]);
  const [next, setNext] = useState<string | null>(null);
  const [failsLoaded, setFailsLoaded] = useState(false);

  const running = !sync || sync.state === "running";
  useEffect(() => {
    let live = true;
    let prev = "";
    const tick = async () => {
      try {
        const s = await getNexusSync(id);
        if (!live) return;
        setSync(s);
        if (prev === "running" && s.state !== "running") onFinished();
        prev = s.state;
      } catch (e) { if (live) setErr((e as Error).message); }
    };
    tick();
    if (!running) return () => { live = false; };
    const t = setInterval(tick, 2000);
    return () => { live = false; clearInterval(t); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, running]);

  const more = async (after?: string) => {
    try {
      const p = await nexusSyncFailures(id, after);
      setFails((f) => (after ? [...f, ...p.items] : p.items));
      setNext(p.next);
      setFailsLoaded(true);
    } catch (e) { setErr((e as Error).message); }
  };
  const totalFailed = sync?.repos?.reduce((n, r) => n + r.failed, 0) ?? 0;
  useEffect(() => {
    if (totalFailed > 0 && sync?.state !== "running") more();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sync?.state, totalFailed > 0]);

  const cancel = async () => {
    if (!confirm("Cancel the sync? Files already imported stay in place.")) return;
    try { await cancelNexusSync(id); setSync(await getNexusSync(id)); } catch (e) { setErr((e as Error).message); }
  };

  if (!sync) return err ? <p className="error">{err}</p> : null;
  return (
    <>
      <div className="card">
        <div className="row">
          <h2>Sync {sync.id}</h2>
          <span className="badge">{sync.state}</span>
          <span className="muted">{fmtTime(sync.started_at)}</span>
          {sync.state === "running" && <button className="danger" onClick={cancel}>Cancel</button>}
        </div>
        {sync.error && <p className="error">{sync.error}</p>}
        {err && <p className="error">{err}</p>}
        <table>
          <thead><tr><th>Nexus repository</th><th>State</th><th>Imported</th><th>Skipped</th><th>Ignored</th><th>Failed</th></tr></thead>
          <tbody>
            {(sync.repos ?? []).map((r) => (
              <tr key={r.source_repo}>
                <td>{r.source_repo}</td><td>{r.state}</td>
                <td>{r.imported}</td><td>{r.skipped}</td><td>{r.ignored}</td><td>{r.failed}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {totalFailed > 0 && (
        <div className="card">
          <strong>Failures</strong>
          {sync.state === "running" && !failsLoaded && <div><button onClick={() => more()}>Show failures</button></div>}
          {fails.length > 0 && (
            <table>
              <thead><tr><th>Nexus repository</th><th>Path</th><th>Reason</th></tr></thead>
              <tbody>{fails.map((f, i) => <tr key={i}><td>{f.source_repo}</td><td><code>{f.path}</code></td><td>{f.reason}</td></tr>)}</tbody>
            </table>
          )}
          {next && <button onClick={() => more(next)}>Load more</button>}
        </div>
      )}
    </>
  );
}
