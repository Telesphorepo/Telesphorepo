import { api, go, useAsync, type Repo } from "../api";

export default function Repos() {
  const { data, error } = useAsync(() => api<Repo[]>("GET", "/api/repos"), []);
  return (
    <>
      <h1>Repositories</h1>
      {error && <p className="error">{error}</p>}
      {data && data.length === 0 && <p className="muted">No repositories visible to you.</p>}
      {data && data.length > 0 && (
        <table>
          <thead><tr><th>Name</th><th>Format</th><th>Access</th><th>Your permission</th></tr></thead>
          <tbody>
            {data.map((r) => (
              <tr key={r.name} className="click" onClick={() => go("/repo/" + encodeURIComponent(r.name))}>
                <td><a href={"#/repo/" + encodeURIComponent(r.name)}>{r.name}</a></td>
                <td>{r.format}</td>
                <td>{r.anonymous_read && <span className="badge">anonymous</span>}</td>
                <td>{r.permission || "none"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}
