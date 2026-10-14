import { useEffect, useState } from "react";
import { cancelMove, getMove, moveFailures, type Move, type MoveFailure } from "../api";

// MovePanel shows one storage move. It polls every 2 s while the move copies.
export default function MovePanel({ initial, onEnd }: { initial: Move; onEnd: (m: Move) => void }) {
  const [move, setMove] = useState(initial);
  const [err, setErr] = useState("");
  const [fails, setFails] = useState<MoveFailure[]>([]);
  const [next, setNext] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);

  useEffect(() => { setMove(initial); setFails([]); setNext(null); setLoaded(false); }, [initial.id]);

  useEffect(() => {
    if (move.state !== "copying") return;
    const t = setInterval(async () => {
      try {
        const m = await getMove(move.id);
        setMove(m);
        if (m.state !== "copying") onEnd(m);
      } catch (e) { setErr((e as Error).message); }
    }, 2000);
    return () => clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [move.id, move.state]);

  const more = async (after?: string) => {
    try {
      const p = await moveFailures(move.id, after);
      setFails((f) => (after ? [...f, ...p.items] : p.items));
      setNext(p.next); setLoaded(true);
    } catch (e) { setErr((e as Error).message); }
  };
  useEffect(() => {
    if (move.state === "failed" && !loaded) more();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [move.state]);

  const cancel = async () => {
    if (!confirm("Cancel the storage move? The active backend stays unchanged.")) return;
    try { await cancelMove(move.id); setMove(await getMove(move.id)); } catch (e) { setErr((e as Error).message); }
  };

  const done = move.copied + move.skipped + move.failed;
  const pct = move.total > 0 ? Math.min(100, Math.round((done / move.total) * 100)) : move.state === "done" ? 100 : 0;
  const label = { copying: "Moving blobs", done: "Move finished", failed: "Move failed", cancelled: "Move cancelled" }[move.state];
  return (
    <div className="card notice">
      <div className="row">
        <strong>{label}</strong>
        <span className="badge">{move.target.storage}{move.target.bucket ? " / " + move.target.bucket : ""}{move.target.fs_root ? " / " + move.target.fs_root : ""}</span>
        {move.state === "copying" && <button className="danger" onClick={cancel}>Cancel</button>}
      </div>
      <div className="bar"><div style={{ width: pct + "%" }} /></div>
      <div className="muted">{done} of {move.total} ({pct}%): {move.copied} copied, {move.skipped} skipped, {move.failed} failed</div>
      {move.error && <p className="error">{move.error}</p>}
      {err && <p className="error">{err}</p>}
      {move.state === "failed" && (
        <>
          {fails.length > 0 && (
            <table>
              <thead><tr><th>Blob</th><th>Reason</th></tr></thead>
              <tbody>{fails.map((f) => <tr key={f.asset_id}><td><code>{f.key}</code></td><td>{f.reason}</td></tr>)}</tbody>
            </table>
          )}
          {next && <button onClick={() => more(next)}>Load more</button>}
        </>
      )}
    </div>
  );
}
