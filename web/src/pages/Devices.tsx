import { useState } from "react";
import { api, fmtTime, usePaged, type Device, type User } from "../api";

const state = (d: Device) =>
  d.revoked_at ? "revoked" : new Date(d.expires_at) < new Date() ? "expired" : "active";

export default function Devices({ me }: { me: User }) {
  const { items, error, more, reload } = usePaged<Device>("/api/devices");
  const [err, setErr] = useState("");
  const revoke = async (d: Device) => {
    if (!confirm(`Revoke ${d.name}? Its open publish sessions end at once.`)) return;
    setErr("");
    try { await api("DELETE", "/api/devices/" + d.id); reload(); } catch (e) { setErr((e as Error).message); }
  };
  return (
    <>
      <h1>Devices</h1>
      <p className="muted">
        A device is a machine that enrolled with <code>./gradlew telesphorepoLogin</code> or <code>mvn telesphorepo:login</code>.
        It publishes with its own key and needs no password.
        {me.is_admin ? " As an administrator you see the devices of all users." : ""}
      </p>
      {(err || error) && <p className="error">{err || error}</p>}
      <table>
        <thead><tr><th>Name</th>{me.is_admin && <th>User</th>}<th>Client</th><th>Enrolled</th><th>Valid until</th><th>Last used</th><th>State</th><th /></tr></thead>
        <tbody>
          {items.map((d) => (
            <tr key={d.id}>
              <td>{d.name}</td>
              {me.is_admin && <td>{d.username}</td>}
              <td>{d.kind}</td>
              <td>{fmtTime(d.created_at)}</td>
              <td>{fmtTime(d.expires_at)}</td>
              <td>{fmtTime(d.last_used_at)}</td>
              <td>{state(d)}</td>
              <td>{!d.revoked_at && <button className="danger" onClick={() => revoke(d)}>Revoke</button>}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {items.length === 0 && !error && <p className="muted">No devices.</p>}
      {more && <button onClick={more}>Load more</button>}
    </>
  );
}
