import { useState } from "react";
import { api, useAsync, type Methods, type Proof } from "../api";
import { getPasskeyAssertion, passkeysSupported } from "../webauthn";

type SecurityState = { totp: { id: number }[]; passkeys: { id: number }[] };

// The second factors the signed-in user has registered. Recovery codes only count at sign-in.
export function useMethods(): Methods | undefined {
  const { data } = useAsync(() => api<SecurityState>("GET", "/api/security"), []);
  return data && { totp: data.totp.length > 0, passkey: data.passkeys.length > 0, recovery: false };
}

// Asks for a fresh second factor and hands the proof to onProof. The caller shows the error.
// passkeyOptions is the route that starts a passkey check: the pending sign-in has its own.
export default function SecondFactor({ methods, label, busy, onProof, passkeyOptions = "/api/security/passkeys/assert" }: {
  methods: Methods; label: string; busy?: boolean; onProof: (p: Proof) => void; passkeyOptions?: string;
}) {
  const [code, setCode] = useState("");
  const [recovery, setRecovery] = useState(false);
  const [err, setErr] = useState("");
  const passkey = methods.passkey && passkeysSupported();
  const useCode = methods.totp || methods.recovery;

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const c = code.trim();
    if (!c) return;
    setCode("");
    onProof(recovery ? { recovery_code: c } : { totp_code: c });
  };
  const usePasskey = async () => {
    setErr("");
    try {
      const opts = await api<never>("POST", passkeyOptions);
      onProof({ passkey: await getPasskeyAssertion(opts) });
    } catch (e) { setErr((e as Error).message); }
  };

  return (
    <>
      {useCode && (
        <form className="row" onSubmit={submit}>
          <input
            className="code" autoFocus inputMode={recovery ? "text" : "numeric"} autoComplete="one-time-code"
            placeholder={recovery ? "Recovery code" : "6-digit code"} value={code} onChange={(e) => setCode(e.target.value)}
          />
          <button className="primary" disabled={busy || !code.trim()}>{label}</button>
          {methods.recovery && methods.totp && (
            <button type="button" className="link" onClick={() => { setRecovery(!recovery); setCode(""); }}>
              {recovery ? "Use authenticator code" : "Use a recovery code"}
            </button>
          )}
        </form>
      )}
      {passkey && <div className="row"><button type="button" disabled={busy} onClick={usePasskey}>{useCode ? "Or use a passkey" : `${label} with a passkey`}</button></div>}
      {!useCode && !passkey && <p className="error">This account has no usable second factor in this browser.</p>}
      {err && <p className="error">{err}</p>}
    </>
  );
}
