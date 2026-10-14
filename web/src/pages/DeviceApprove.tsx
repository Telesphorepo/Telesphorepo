import { useEffect, useState } from "react";
import { api, ApiError, type Proof, type User } from "../api";
import SecondFactor, { useMethods } from "./SecondFactor";

type Enrollment = { user_code: string; name: string; kind: "gradle" | "maven"; expires_at: string };

const normalize = (s: string) => s.toUpperCase().replace(/[^A-Z0-9]/g, "").replace(/^(.{4})(.+)$/, "$1-$2");

const initialCode = () => {
  const h = window.location.hash;
  const i = h.indexOf("?");
  return normalize(i < 0 ? "" : new URLSearchParams(h.slice(i + 1)).get("code") ?? "");
};

function Approve({ enrollment, onDone }: { enrollment: Enrollment; onDone: () => void }) {
  const methods = useMethods();
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const approve = async (proof: Proof) => {
    setErr(""); setBusy(true);
    try {
      await api("POST", "/api/devices/approve", { user_code: enrollment.user_code, ...proof });
      onDone();
    } catch (e) { setErr((e as Error).message); }
    setBusy(false);
  };
  return (
    <div className="card">
      <p>A device asks to publish with your account.</p>
      <table>
        <tbody>
          <tr><th>Code</th><td className="mono">{enrollment.user_code}</td></tr>
          <tr><th>Name</th><td>{enrollment.name}</td></tr>
          <tr><th>Client</th><td>{enrollment.kind}</td></tr>
        </tbody>
      </table>
      <p className="muted">Approve only if this code is the one your terminal shows. Confirm with your second factor.</p>
      {methods && <SecondFactor methods={methods} label="Approve" busy={busy} onProof={approve} />}
      {err && <p className="error">{err}</p>}
    </div>
  );
}

export default function DeviceApprove({ me }: { me: User | null }) {
  const [code, setCode] = useState(initialCode);
  const [enrollment, setEnrollment] = useState<Enrollment>();
  const [approved, setApproved] = useState(false);
  const [err, setErr] = useState("");

  const lookup = async (c: string) => {
    setErr(""); setEnrollment(undefined);
    try { setEnrollment(await api<Enrollment>("GET", "/api/devices/enroll?user_code=" + encodeURIComponent(c))); }
    catch (e) { setErr(e instanceof ApiError && e.status === 404 ? "This code is unknown or expired." : (e as Error).message); }
  };
  useEffect(() => {
    if (me && code.length === 9) lookup(code);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [me]);

  if (!me) {
    const next = encodeURIComponent("/device" + (code ? "?code=" + code : ""));
    return (
      <>
        <h1>Approve a device</h1>
        <p className="muted">Please <a href={"#/login?next=" + next}>log in</a> to approve a device.</p>
      </>
    );
  }
  return (
    <>
      <h1>Approve a device</h1>
      {approved ? (
        <div className="card notice"><p>The device is approved. You can return to your terminal.</p></div>
      ) : (
        <>
          <form className="row" onSubmit={(e) => { e.preventDefault(); lookup(code); }}>
            <input className="code" placeholder="ABCD-EFGH" required value={code} onChange={(e) => setCode(normalize(e.target.value))} />
            <button className="primary" disabled={code.length !== 9}>Look up</button>
          </form>
          {err && <p className="error">{err}</p>}
          {enrollment && <Approve enrollment={enrollment} onDone={() => setApproved(true)} />}
        </>
      )}
    </>
  );
}
