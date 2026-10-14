import { useEffect, useState } from "react";
import { api, type Methods, type Proof } from "../api";
import SecondFactor from "./SecondFactor";

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

// POST /api/auth/login answers {second_factor: true, methods: [...]} when the account has a second
// factor, and the user otherwise (the session is then setup-only).
type LoginAnswer = { second_factor?: boolean; methods?: string[] };

export default function Login({ onDone }: { onDone: () => void }) {
  const [username, setU] = useState("");
  const [password, setP] = useState("");
  const [err, setErr] = useState(hashError);
  const [oidc, setOidc] = useState(false);
  const [methods, setMethods] = useState<Methods>();
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    api<{ oidc?: boolean }>("GET", "/api/auth/config").then((c) => setOidc(!!c.oidc), () => {});
  }, []);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setErr("");
    try {
      const a = await api<LoginAnswer | undefined>("POST", "/api/auth/login", { username, password });
      if (a?.second_factor) {
        const m = a.methods ?? [];
        setMethods({ totp: m.includes("totp"), passkey: m.includes("passkey"), recovery: m.includes("recovery") });
        setP("");
      } else onDone();
    } catch (e) { setErr((e as Error).message); }
  };
  const second = async (proof: Proof) => {
    setErr(""); setBusy(true);
    try { await api("POST", "/api/auth/login/second", proof); onDone(); }
    catch (e) { setErr((e as Error).message); }
    setBusy(false);
  };

  if (methods) {
    return (
      <div className="card login">
        <h1>Second step</h1>
        <p className="muted">Signing in as {username}. Enter a code from your authenticator app, or use a passkey.</p>
        <SecondFactor methods={methods} label="Verify" busy={busy} onProof={second} passkeyOptions="/api/auth/login/passkey" />
        {err && <p className="error">{err}</p>}
        <button type="button" className="link" onClick={() => { setMethods(undefined); setErr(""); }}>Back</button>
      </div>
    );
  }
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
