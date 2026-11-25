import { useCallback, useEffect, useState, type MouseEvent } from "react";
import { api, setUnauthorizedHandler, type Me } from "./api";
import { Audit } from "./pages/Audit";
import { Keys } from "./pages/Keys";
import { Login } from "./pages/Login";
import { Overview } from "./pages/Overview";

type Route = "/" | "/keys" | "/audit";
const routes: Route[] = ["/", "/keys", "/audit"];

function currentRoute(): Route {
  const p = window.location.pathname as Route;
  return routes.includes(p) ? p : "/";
}

export function App() {
  const [me, setMe] = useState<Me | null | undefined>(undefined); // undefined = still checking
  const [route, setRoute] = useState<Route>(currentRoute);

  useEffect(() => {
    setUnauthorizedHandler(() => setMe(null));
    api.me().then(setMe, () => setMe(null));
    const onPop = () => setRoute(currentRoute());
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  const go = useCallback((to: Route) => (e: MouseEvent) => {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return; // let the browser open new tabs
    e.preventDefault();
    window.history.pushState(null, "", to);
    setRoute(to);
  }, []);

  async function signOut() {
    await api.logout().catch(() => {});
    setMe(null);
  }

  if (me === undefined) return null;
  if (me === null) return <Login onSignedIn={setMe} />;

  // Viewers can't read the audit log; send them to the overview instead.
  const page = route === "/audit" && me.role !== "admin" ? "/" : route;
  const nav: [Route, string][] = [
    ["/", "Overview"],
    ["/keys", "Keys"],
    ...(me.role === "admin" ? ([["/audit", "Audit log"]] as [Route, string][]) : []),
  ];

  return (
    <>
      <header className="topbar">
        <a className="brand" href="/" onClick={go("/")}>
          <span className="brand-mark" aria-hidden="true" />
          Gateway Console
        </a>
        <nav aria-label="Main">
          {nav.map(([to, label]) => (
            <a key={to} href={to} onClick={go(to)} aria-current={page === to ? "page" : undefined}>
              {label}
            </a>
          ))}
        </nav>
        <div className="user">
          <span>{me.username}</span>
          <span className={`badge badge-${me.role}`}>{me.role}</span>
          <button className="link" onClick={signOut}>
            Sign out
          </button>
        </div>
      </header>
      <main>
        {page === "/" && <Overview />}
        {page === "/keys" && <Keys role={me.role} />}
        {page === "/audit" && <Audit />}
      </main>
    </>
  );
}
