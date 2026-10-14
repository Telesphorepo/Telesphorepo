import { useState } from "react";
import { api } from "../api";

export default function Login({ onDone }: { onDone: () => void }) {
  const [username, setU] = useState("");
  const [password, setP] = useState("");
  const [err, setErr] = useState("");
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    try { await api("POST", "/api/auth/login", { username, password }); onDone(); }
    catch (e) { setErr((e as Error).message); }
  };
  return (
    <form className="card login" onSubmit={submit}>
      <h1>Telesphorepo</h1>
      <label>Username<input autoFocus value={username} onChange={(e) => setU(e.target.value)} autoComplete="username" /></label>
      <label>Password<input type="password" value={password} onChange={(e) => setP(e.target.value)} autoComplete="current-password" /></label>
      {err && <p className="error">{err}</p>}
      <button className="primary">Log in</button>
    </form>
  );
}
