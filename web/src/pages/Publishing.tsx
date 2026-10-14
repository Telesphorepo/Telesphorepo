import { useState } from "react";
import { api, fmtTime, useAsync, type Proof, type PublishSession, type Repo, type User } from "../api";
import SecondFactor, { useMethods } from "./SecondFactor";

const HOST = window.location.origin;
const USER = "publish";

function Copy({ text, label = "Copy" }: { text: string; label?: string }) {
  const [done, setDone] = useState(false);
  return <button onClick={async () => { await navigator.clipboard.writeText(text); setDone(true); }}>{done ? "Copied" : label}</button>;
}

function Snippet({ title, code }: { title: string; code: string }) {
  return (
    <details>
      <summary>{title}</summary>
      <pre>{code}</pre>
      <Copy text={code} />
    </details>
  );
}

const repoUrl = (r: Repo) => `${HOST}/${r.format}/${r.name}`;

function sessionSnippets(r: Repo, token: string) {
  const url = repoUrl(r);
  const out: { title: string; code: string }[] = [];
  if (r.format === "maven") {
    out.push({
      title: "Maven: settings.xml",
      code: `<settings>
  <servers>
    <server>
      <id>telesphorepo</id>
      <username>${USER}</username>
      <password>${token}</password>
    </server>
  </servers>
</settings>
<!-- pom.xml: <distributionManagement><repository><id>telesphorepo</id><url>${url}</url></repository></distributionManagement> -->`,
    }, {
      title: "Gradle: publishing block",
      code: `publishing {
    repositories {
        maven {
            url = uri("${url}")
            credentials {
                username = "${USER}"
                password = "${token}"
            }
        }
    }
}`,
    });
  }
  const path = r.format === "debian" ? `${url}/upload/DIST/COMPONENT` : `${url}/PATH/FILE`;
  out.push({ title: "curl", code: `curl -f -u ${USER}:${token} -T FILE ${path}` });
  return out;
}

function NewSession({ repos }: { repos: Repo[] }) {
  const methods = useMethods();
  const [repo, setRepo] = useState(repos[0]?.name ?? "");
  const [session, setSession] = useState<PublishSession>();
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const current = repos.find((r) => r.name === repo) ?? repos[0];

  const create = async (proof: Proof) => {
    setErr(""); setBusy(true);
    try { setSession(await api<PublishSession>("POST", "/api/publish/sessions/manual", { repo: current.name, ...proof })); }
    catch (e) { setErr((e as Error).message); }
    setBusy(false);
  };
  const end = async () => {
    if (!session) return;
    try {
      // The session token itself ends the session.
      await fetch("/api/publish/sessions/" + encodeURIComponent(session.id), {
        method: "DELETE", credentials: "omit", headers: { Authorization: "Basic " + btoa(`${USER}:${session.token}`) },
      });
    } catch { /* the session expires on its own */ }
    setSession(undefined);
  };

  if (repos.length === 0) return <p className="muted">You have no repository with write access.</p>;
  if (session) {
    const r = repos.find((x) => x.name === session.repo) ?? current;
    return (
      <div className="card notice">
        <p>Publish session for <strong>{session.repo}</strong>. It ends at {fmtTime(session.expires_at)}. Copy the token now. It will not be shown again.</p>
        <div className="row"><code className="mono grow">{session.token}</code><Copy text={session.token} /></div>
        <p className="muted">Use it as the password with any username, for example <code>{USER}</code>.</p>
        {sessionSnippets(r, session.token).map((s) => <Snippet key={s.title} {...s} />)}
        <div className="row"><button className="danger" onClick={end}>End session now</button></div>
      </div>
    );
  }
  return (
    <div className="card">
      <div className="row">
        <label className="inline">Repository
          <select value={current.name} onChange={(e) => setRepo(e.target.value)}>
            {repos.map((r) => <option key={r.name} value={r.name}>{r.name} ({r.format})</option>)}
          </select>
        </label>
      </div>
      <p className="muted">Confirm with your second factor. The token works for this repository only and for a short time.</p>
      {methods && (methods.totp || methods.passkey
        ? <SecondFactor methods={methods} label="Create session" busy={busy} onProof={create} />
        : <p className="error">Add a second factor on the <a href="#/security">Security</a> page first.</p>)}
      {err && <p className="error">{err}</p>}
    </div>
  );
}

function CiSnippets({ repos }: { repos: Repo[] }) {
  const repo = repos[0]?.name ?? "REPO";
  const exchange = (oidc: string) => `SESSION=$(curl -fsS -X POST ${HOST}/api/publish/sessions/oidc \\
  -H 'Content-Type: application/json' \\
  -d "{\\"token\\":\\"${oidc}\\",\\"repo\\":\\"${repo}\\"}")
TOKEN=$(echo "$SESSION" | jq -r .token)
ID=$(echo "$SESSION" | jq -r .id)`;
  const close = `curl -fsS -X DELETE -u ${USER}:$TOKEN ${HOST}/api/publish/sessions/$ID`;
  return (
    <>
      <p className="muted">
        A CI job proves its identity with the signed token of the CI system. An administrator must add a trust policy first.
        The exchange answers <code>{"{id, token, repo, expires_at}"}</code>. Use <code>token</code> as the password, then end the session.
      </p>
      <Snippet title="GitHub Actions" code={`# the job needs: permissions: { id-token: write }
OIDC=$(curl -fsS -H "Authorization: Bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" \\
  "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=${HOST}" | jq -r .value)
${exchange("$OIDC")}
curl -f -u ${USER}:$TOKEN -T FILE ${HOST}/raw/${repo}/PATH/FILE
${close}`} />
      <Snippet title="Jenkins" code={`// Jenkins with the OpenID Connect Provider plugin: bind an "OpenID Connect id token"
// credential (audience ${HOST}) to the variable OIDC_TOKEN.
sh '''
${exchange("$OIDC_TOKEN")}
curl -f -u ${USER}:$TOKEN -T FILE ${HOST}/raw/${repo}/PATH/FILE
${close}
'''`} />
    </>
  );
}

function Clients() {
  return (
    <>
      <p className="muted">
        The Gradle plugin and the Maven extension create a publish session for every publish and end it afterwards.
        Use the latest release in place of <code>VERSION</code>.
      </p>
      <Snippet title="Gradle plugin" code={`// settings.gradle.kts
plugins { id("com.telesphorepo") version "VERSION" }
telesphorepo { url = "${HOST}" }

// Once per machine. It prints a code. Approve it at ${HOST}/#/device.
./gradlew telesphorepoLogin

// In CI the plugin uses the CI identity token instead (see "CI" below).`} />
      <Snippet title="Maven extension" code={`<!-- .mvn/extensions.xml -->
<extensions>
  <extension>
    <groupId>com.telesphorepo</groupId>
    <artifactId>telesphorepo-maven-extension</artifactId>
    <version>VERSION</version>
  </extension>
</extensions>

# .mvn/maven.config
-Dtelesphorepo.url=${HOST}

# Once per machine. It prints a code. Approve it at ${HOST}/#/device.
mvn com.telesphorepo:telesphorepo-maven-plugin:VERSION:login -Dtelesphorepo.url=${HOST}

# settings.xml needs no password for this server.`} />
    </>
  );
}

export default function Publishing({ me }: { me: User }) {
  const repos = useAsync(() => api<Repo[]>("GET", "/api/repos"), []);
  const writable = (repos.data ?? []).filter((r) => me.is_admin || r.permission === "write" || r.permission === "admin");
  return (
    <>
      <h1>Publishing</h1>
      <p className="muted">
        Writes need a publish session. A password or a read token can only read.
        A publish session is made for one repository and ends after a short time.
      </p>
      {repos.error && <p className="error">{repos.error}</p>}
      <h2>New publish session</h2>
      {repos.data && <NewSession repos={writable} />}
      <h2>Gradle and Maven</h2>
      <Clients />
      <h2>CI</h2>
      <CiSnippets repos={writable} />
    </>
  );
}
