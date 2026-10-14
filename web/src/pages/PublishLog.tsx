import { fmtTime, usePaged, type PublishLogEntry } from "../api";

const state = (e: PublishLogEntry) =>
  e.closed_at ? "ended" : new Date(e.expires_at) < new Date() ? "expired" : "open";

const who = (e: PublishLogEntry) =>
  e.via === "oidc" ? `${e.trust_name ?? "deleted policy"}: ${e.subject}`
    : e.via === "device" ? `${e.username ?? "deleted user"} on ${e.device_name ?? "deleted device"}`
      : e.username ?? "deleted user";

export default function PublishLog() {
  const { items, error, more } = usePaged<PublishLogEntry>("/api/publish/log");
  return (
    <>
      <h1>Publish log</h1>
      <p className="muted">Every publish session, newest first. The server keeps ended sessions for 30 days.</p>
      {error && <p className="error">{error}</p>}
      <table>
        <thead><tr><th>Started</th><th>Repository</th><th>Via</th><th>Who</th><th>Ended</th><th>State</th></tr></thead>
        <tbody>
          {items.map((e) => (
            <tr key={e.id}>
              <td>{fmtTime(e.created_at)}</td><td>{e.repo}</td><td>{e.via}</td><td>{who(e)}</td>
              <td>{e.closed_at ? fmtTime(e.closed_at) : ""}</td><td>{state(e)}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {items.length === 0 && !error && <p className="muted">No publish sessions.</p>}
      {more && <button onClick={more}>Load more</button>}
    </>
  );
}
