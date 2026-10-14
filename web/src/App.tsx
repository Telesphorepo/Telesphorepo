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
    window.addEventListener("unauthorized", f);
    return () => window.removeEventListener("unauthorized", f);
  }, []);

  if (!ready) return null;

  // Anonymous visitors may still browse repos with anonymous read.
  const loginRoute = route === "/login" || route.startsWith("/login?");
  if (loginRoute) return <Login onDone={async () => { await refresh(); go("/"); }} />;

  const logout = async () => { try { await api("POST", "/api/auth/logout"); } catch { /* ignore */ } setMe(null); go("/login"); };
  const [, section = "", arg = ""] = route.split("/");
  const link = (to: string, label: string) => (
    <a href={"#" + to} className={route === to || (to !== "/" && route.startsWith(to + "/")) ? "active" : ""}>{label}</a>
  );

  let page;
  if (section === "repo") page = <Browser name={decodeURIComponent(arg)} />;
  else if (section === "search") page = <Search />;
  else if (section === "tokens") page = me ? <Tokens /> : <Anon />;
  else if (section === "admin") page = me?.is_admin ? <Admin /> : <p className="muted">Admin access required.</p>;
  else if (section === "settings") page = me?.is_admin ? <Settings /> : <p className="muted">Admin access required.</p>;
  else if (section === "nexus") page = me?.is_admin ? <Nexus id={arg ? Number(arg) : 0} /> : <p className="muted">Admin access required.</p>;
  else page = <Repos />;

  return (
    <>
      <header>
        <strong className="brand">Telesphorepo</strong>
        <nav>
          {link("/", "Repositories")}
          {link("/search", "Search")}
          {me && link("/tokens", "Tokens")}
          {me?.is_admin && link("/admin", "Admin")}
          {me?.is_admin && link("/nexus", "Nexus")}
          {me?.is_admin && link("/settings", "Settings")}
        </nav>
        <span className="spacer" />
        {me ? (
          <span>{me.username} <button className="link" onClick={logout}>Log out</button></span>
        ) : (
          <a href="#/login">Log in</a>
        )}
      </header>
      <main>{page}</main>
    </>
  );
}

const Anon = () => <p className="muted">Please <a href="#/login">log in</a>.</p>;
