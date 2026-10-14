import { useState } from "react";
import { api, fmtTime, useAsync } from "../api";
import { QR } from "../qr";
import { createPasskey, passkeysSupported } from "../webauthn";

type State = {
  totp: { id: number; name: string; created_at: string }[];
  passkeys: { id: number; name: string; created_at: string; last_used_at: string | null }[];
  recovery_codes: number;
};
type TotpSetup = { setup_id: string; secret: string; otpauth_url: string };

function Totp({ state, run }: { state: State; run: (fn: () => Promise<unknown>) => Promise<void> }) {
  const [name, setName] = useState("");
  const [setup, setSetup] = useState<TotpSetup>();
  const [code, setCode] = useState("");

  const begin = (e: React.FormEvent) => {
    e.preventDefault();
    return run(async () => setSetup(await api<TotpSetup>("POST", "/api/security/totp/begin", { name })));
  };
  const confirm = (e: React.FormEvent) => {
    e.preventDefault();
    return run(async () => {
      await api("POST", "/api/security/totp/confirm", { setup_id: setup!.setup_id, code: code.trim() });
      setSetup(undefined); setName(""); setCode("");
    });
  };
  return (
    <section>
      <h2>Authenticator app</h2>
      <table>
        <thead><tr><th>Name</th><th>Added</th><th /></tr></thead>
        <tbody>
          {state.totp.map((t) => (
            <tr key={t.id}>
              <td>{t.name}</td><td>{fmtTime(t.created_at)}</td>
              <td><button className="danger" onClick={() => confirmDelete("authenticator") && run(() => api("DELETE", "/api/security/totp/" + t.id))}>Remove</button></td>
            </tr>
          ))}
        </tbody>
      </table>
      {state.totp.length === 0 && <p className="muted">No authenticator app.</p>}
      {!setup && (
        <form className="row" onSubmit={begin}>
          <input placeholder="Name, for example Phone" required value={name} onChange={(e) => setName(e.target.value)} />
          <button className="primary">Add authenticator app</button>
        </form>
      )}
      {setup && (
        <form className="card notice" onSubmit={confirm}>
          <p>Scan the code with your authenticator app, then enter the 6-digit code it shows.</p>
          <div className="row">
            <QR text={setup.otpauth_url} />
            <div className="grow">
              <p className="muted">Cannot scan? Enter this key by hand:</p>
              <code className="mono">{setup.secret}</code>
              <p className="muted">Or open this link on the device with the app:</p>
              <a className="mono" href={setup.otpauth_url}>{setup.otpauth_url}</a>
            </div>
          </div>
          <div className="row">
            <input className="code" autoFocus inputMode="numeric" autoComplete="one-time-code" placeholder="6-digit code" value={code} onChange={(e) => setCode(e.target.value)} />
            <button className="primary" disabled={!code.trim()}>Confirm</button>
            <button type="button" onClick={() => { setSetup(undefined); setCode(""); }}>Cancel</button>
          </div>
        </form>
      )}
    </section>
  );
}

function Passkeys({ state, run }: { state: State; run: (fn: () => Promise<unknown>) => Promise<void> }) {
  const [name, setName] = useState("");
  const add = (e: React.FormEvent) => {
    e.preventDefault();
    return run(async () => {
      const options = await api<never>("POST", "/api/security/passkeys/begin", { name });
      const credential = await createPasskey(options);
      await api("POST", "/api/security/passkeys/finish", { name, credential });
      setName("");
    });
  };
  return (
    <section>
      <h2>Passkeys</h2>
      <table>
        <thead><tr><th>Name</th><th>Added</th><th>Last used</th><th /></tr></thead>
        <tbody>
          {state.passkeys.map((p) => (
            <tr key={p.id}>
              <td>{p.name}</td><td>{fmtTime(p.created_at)}</td><td>{fmtTime(p.last_used_at)}</td>
              <td><button className="danger" onClick={() => confirmDelete("passkey") && run(() => api("DELETE", "/api/security/passkeys/" + p.id))}>Remove</button></td>
            </tr>
          ))}
        </tbody>
      </table>
      {state.passkeys.length === 0 && <p className="muted">No passkeys.</p>}
      {passkeysSupported() ? (
        <form className="row" onSubmit={add}>
          <input placeholder="Name, for example Laptop" required value={name} onChange={(e) => setName(e.target.value)} />
          <button className="primary">Add passkey</button>
        </form>
      ) : (
        <p className="muted">This browser cannot create passkeys. Passkeys need HTTPS or localhost.</p>
      )}
    </section>
  );
}

function Recovery({ state, run }: { state: State; run: (fn: () => Promise<unknown>) => Promise<void> }) {
  const [codes, setCodes] = useState<string[]>();
  const [copied, setCopied] = useState(false);
  const none = state.totp.length + state.passkeys.length === 0;
  const generate = () => {
    if (state.recovery_codes > 0 && !confirm("This replaces your current recovery codes. Continue?")) return;
    return run(async () => {
      const r = await api<{ codes: string[] }>("POST", "/api/security/recovery-codes");
      setCodes(r.codes); setCopied(false);
    });
  };
  return (
    <section>
      <h2>Recovery codes</h2>
      <p className="muted">
        Each code signs you in once if you lose your authenticator or passkey.
        {state.recovery_codes > 0 ? ` ${state.recovery_codes} of 10 codes are unused.` : " You have no recovery codes."}
      </p>
      {codes && (
        <div className="card notice">
          <p>Save these codes now. They will not be shown again.</p>
          <pre>{codes.join("\n")}</pre>
          <div className="row">
            <button onClick={async () => { await navigator.clipboard.writeText(codes.join("\n")); setCopied(true); }}>{copied ? "Copied" : "Copy"}</button>
            <button onClick={() => setCodes(undefined)}>Dismiss</button>
          </div>
        </div>
      )}
      <button disabled={none} onClick={generate}>{state.recovery_codes > 0 ? "Generate new codes" : "Generate codes"}</button>
      {none && <span className="muted"> Add an authenticator app or a passkey first.</span>}
    </section>
  );
}

const confirmDelete = (what: string) => confirm(`Remove this ${what}? You may not be able to sign in with it again.`);

// onChange runs after every change so that the app reloads the account state, which lifts the
// setup-only restriction once the first second factor exists.
export default function Security({ setupOnly, onChange }: { setupOnly: boolean; onChange: () => void }) {
  const { data, error, reload } = useAsync(() => api<State>("GET", "/api/security"), []);
  const [err, setErr] = useState("");
  const run = async (fn: () => Promise<unknown>) => {
    setErr("");
    try { await fn(); reload(); onChange(); } catch (e) { setErr((e as Error).message); }
  };
  return (
    <>
      <h1>Security</h1>
      {setupOnly && (
        <div className="card notice">
          <p><strong>Set up a second factor to continue.</strong></p>
          <p>Add an authenticator app or a passkey. Until you do, this is the only page you can use.</p>
        </div>
      )}
      {(err || error) && <p className="error">{err || error}</p>}
      {data && (
        <>
          <Totp state={data} run={run} />
          <Passkeys state={data} run={run} />
          <Recovery state={data} run={run} />
        </>
      )}
    </>
  );
}
