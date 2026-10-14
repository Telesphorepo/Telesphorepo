import { useCallback, useEffect, useState } from "react";
import { api, go, useHash, type User } from "./api";
import Login from "./pages/Login";
import Repos from "./pages/Repos";
import Browser from "./pages/Browser";
import Search from "./pages/Search";
import Tokens from "./pages/Tokens";
import Admin from "./pages/Admin";
import Settings from "./pages/Settings";
import Nexus from "./pages/Nexus";
import Security from "./pages/Security";
import Devices from "./pages/Devices";
import DeviceApprove from "./pages/DeviceApprove";
import Publishing from "./pages/Publishing";
import Trusts from "./pages/Trusts";
import PublishLog from "./pages/PublishLog";

export default function App() {
  const [me, setMe] = useState<User | null>(null);
  const [ready, setReady] = useState(false);
  const route = useHash();

  const refresh = useCallback(async () => {
    try { setMe(await api<User>("GET", "/api/me")); } catch { setMe(null); }
    setReady(true);
  }, []);
  useEffect(() => { refresh(); }, [refresh]);
  useEffect(() => {
    const f = () => { setMe(null); setReady(true); };
    const g = () => setMe((m) => (m ? { ...m, second_factor_required: true } : m));
    window.addEventListener("unauthorized", f);
    window.addEventListener("secondfactor", g);
    return () => { window.removeEventListener("unauthorized", f); window.removeEventListener("secondfactor", g); };
  }, []);

  if (!ready) return null;

  const [path, query = ""] = route.split("?");

  // Anonymous visitors may still browse repos with anonymous read.
  if (path === "/login") {
    const next = new URLSearchParams(query).get("next");
    return <Login onDone={async () => { await refresh(); go(next && next.startsWith("/") ? next : "/"); }} />;
  }

  const logout = async () => { try { await api("POST", "/api/auth/logout"); } catch { /* ignore */ } setMe(null); go("/login"); };
  const [, section = "", arg = ""] = path.split("/");
  const link = (to: string, label: string) => (
    <a href={"#" + to} className={path === to || (to !== "/" && path.startsWith(to + "/")) ? "active" : ""}>{label}</a>
  );
  const account = me ? (
    <span>{me.username} <button className="link" onClick={logout}>Log out</button></span>
  ) : (
    <a href="#/login">Log in</a>
  );

  // A local account without a second factor can only set one up.
  if (me?.second_factor_required) {
    return (
      <>
        <header>
          <strong className="brand">Telesphorepo</strong>
          <span className="spacer" />
          {account}
        </header>
        <main><Security setupOnly onChange={refresh} /></main>
      </>
    );
  }

  const admin = (page: React.ReactNode) => (me?.is_admin ? page : <p className="muted">Admin access required.</p>);
  let page;
  if (section === "repo") page = <Browser name={decodeURIComponent(arg)} />;
  else if (section === "search") page = <Search />;
  else if (section === "tokens") page = me ? <Tokens /> : <Anon />;
  else if (section === "security") page = me ? <Security setupOnly={false} onChange={refresh} /> : <Anon />;
  else if (section === "devices") page = me ? <Devices me={me} /> : <Anon />;
  else if (section === "device") page = <DeviceApprove me={me} />;
  else if (section === "publishing") page = me ? <Publishing me={me} /> : <Anon />;
  else if (section === "trusts") page = admin(<Trusts />);
  else if (section === "publish-log") page = admin(<PublishLog />);
  else if (section === "admin") page = admin(<Admin />);
  else if (section === "settings") page = admin(<Settings />);
  else if (section === "nexus") page = admin(<Nexus id={arg ? Number(arg) : 0} />);
  else page = <Repos />;

  return (
    <>
      <header>
        <strong className="brand">Telesphorepo</strong>
        <nav>
          {link("/", "Repositories")}
          {link("/search", "Search")}
          {me && link("/publishing", "Publishing")}
          {me && link("/devices", "Devices")}
          {me && link("/tokens", "Read tokens")}
          {me && link("/security", "Security")}
          {me?.is_admin && link("/admin", "Admin")}
          {me?.is_admin && link("/trusts", "Trust policies")}
          {me?.is_admin && link("/publish-log", "Publish log")}
          {me?.is_admin && link("/nexus", "Nexus")}
          {me?.is_admin && link("/settings", "Settings")}
        </nav>
        <span className="spacer" />
        {account}
      </header>
      <main>{page}</main>
    </>
  );
}

const Anon = () => <p className="muted">Please <a href="#/login">log in</a>.</p>;
