import { useEffect, useState } from "react";
import { api, ApiError, type AuditEvent } from "../api";
import { fmt } from "../format";

const labels: Record<string, string> = {
  login: "Signed in",
  "login.failed": "Failed sign-in",
  "key.create": "Created key",
  "key.update": "Changed key",
  "key.revoke": "Revoked key",
};

export function Audit() {
  const [events, setEvents] = useState<AuditEvent[] | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    api
      .audit()
      .then((r) => setEvents(r.events))
      .catch((e: unknown) => setError(e instanceof ApiError ? e.message : "Couldn't load the audit log."));
  }, []);

  return (
    <section className="page">
      <header className="page-head">
        <h1>Audit log</h1>
        <span className="muted">Last 200 events</span>
      </header>
      {error && (
        <p className="banner banner-bad" role="alert">
          {error}
        </p>
      )}
      {events && events.length === 0 && <p className="empty">Nothing yet.</p>}
      {events && events.length > 0 && (
        <div className="card table-wrap">
          <table>
            <thead>
              <tr>
                <th>When</th>
                <th>Who</th>
                <th>What</th>
                <th>Key</th>
                <th>Details</th>
              </tr>
            </thead>
            <tbody>
              {events.map((e, i) => (
                <tr key={`${e.at}-${i}`} className={e.action === "login.failed" ? "row-warn" : ""}>
                  <td className="nowrap">{fmt.dateTime(e.at)}</td>
                  <td>{e.actor}</td>
                  <td>{labels[e.action] ?? e.action}</td>
                  <td>{e.target ?? ""}</td>
                  <td className="muted">{e.detail ?? ""}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
