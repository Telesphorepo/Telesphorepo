import { useEffect, useState } from "react";
import { api } from "../api";

const errorText: Record<string, string> = {
  not_authorized: "Your SSO account is not an administrator here.",
  username_conflict: "A local account with the same username already exists, so this SSO account cannot be used.",
  invalid_username: "Your SSO account has a username that is not valid here.",
  account_disabled: "This account is disabled.",
  sso_failed: "Single sign-on failed. Please try again.",
};

const hashError = () => {
  const h = window.location.hash;
  const i = h.indexOf("?");
  const code = i < 0 ? null : new URLSearchParams(h.slice(i + 1)).get("error");
  return code ? errorText[code] ?? "Sign-in failed. Please try again." : "";
};

export default function Login({ onDone }: { onDone: () => void }) {
  const [username, setU] = useState("");
  const [password, setP] = useState("");
  const [err, setErr] = useState(hashError);
  const [oidc, setOidc] = useState(false);
  useEffect(() => {
    api<{ oidc?: boolean }>("GET", "/api/auth/config").then((c) => setOidc(!!c.oidc), () => {});
  }, []);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    try { await api("POST", "/api/auth/login", { username, password }); onDone(); }
    catch (e) { setErr((e as Error).message); }
  };
  return (
    <form className="card login" onSubmit={submit}>
      <h1>Telesphorepo</h1>
      {oidc && (
        <>
          <button type="button" className="primary" onClick={() => { window.location.href = "/api/auth/oidc/login"; }}>Sign in with SSO</button>
          <p className="muted">Or sign in with a local or publisher account:</p>
        </>
      )}
      <label>Username<input autoFocus={!oidc} value={username} onChange={(e) => setU(e.target.value)} autoComplete="username" /></label>
      <label>Password<input type="password" value={password} onChange={(e) => setP(e.target.value)} autoComplete="current-password" /></label>
      {err && <p className="error">{err}</p>}
      <button className="primary">Log in</button>
    </form>
  );
}
