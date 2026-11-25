import { useEffect, useState } from "react";
import { api, ApiError, type Overview as Data } from "../api";
import { BarChart } from "../components/BarChart";
import { Stat } from "../components/Stat";
import { fmt } from "../format";

function sortedEntries(m: Record<string, number>): [string, number][] {
  return Object.entries(m).sort((a, b) => b[1] - a[1]);
}

export function Overview() {
  const [data, setData] = useState<Data | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    let live = true;
    const load = () =>
      api
        .overview()
        .then((d) => {
          if (!live) return;
          setData(d);
          setError("");
        })
        .catch((e: unknown) => live && setError(e instanceof ApiError ? e.message : "Couldn't load metrics."));
    load();
    const timer = window.setInterval(load, 30_000);
    return () => {
      live = false;
      window.clearInterval(timer);
    };
  }, []);

  if (error && !data) {
    return (
      <section className="page">
        <h1>Overview</h1>
        <p className="banner banner-bad" role="alert">
          {error} Keys still work; see the Keys page.
        </p>
      </section>
    );
  }
  if (!data) {
    return (
      <section className="page">
        <h1>Overview</h1>
        <p className="muted">Loading…</p>
      </section>
    );
  }

  const w = `last ${data.window_hours}h`;
  const keys = sortedEntries(data.spend_by_key);
  const models = sortedEntries(data.requests_by_model);
  const maxKey = keys[0]?.[1] ?? 0;

  return (
    <section className="page">
      <header className="page-head">
        <h1>Overview</h1>
        <span className="muted">Updated {fmt.dateTime(data.generated_at)}</span>
      </header>

      {error && <p className="banner banner-warn">{error} Showing the last good numbers.</p>}
      {data.open_breakers.length > 0 && (
        <p className="banner banner-bad" role="alert">
          Circuit breaker open for {data.open_breakers.join(", ")}: traffic is failing over to the next provider.
        </p>
      )}

      <div className="stats">
        <Stat label="Spend this month" value={fmt.usd(data.spend_month_usd)} hint="Estimated, all keys" />
        <Stat label="Requests" value={fmt.count(data.requests)} hint={w} />
        <Stat
          label="Error rate"
          value={fmt.pct(data.error_rate)}
          hint={w}
          tone={data.error_rate > 0.05 ? "bad" : data.error_rate > 0.01 ? "warn" : "default"}
        />
        <Stat label="Cache hit rate" value={fmt.pct(data.cache_hit_rate)} hint={w} />
        <Stat label="Fallbacks" value={fmt.count(data.fallbacks)} hint={w} />
        <Stat label="p95 latency" value={fmt.seconds(data.p95_seconds)} hint="last 1h" tone={data.p95_seconds > 10 ? "warn" : "default"} />
      </div>

      <div className="grid-2">
        <div className="card">
          <BarChart title="Requests per hour" points={data.requests_by_hour} label={fmt.hour} value={fmt.count} labelEvery={4} />
        </div>
        <div className="card">
          <BarChart title="Spend per day" points={data.spend_by_day} label={fmt.day} value={fmt.usd} labelEvery={2} />
        </div>
      </div>

      <div className="grid-2">
        <div className="card">
          <h2>Spend by key this month</h2>
          {keys.length === 0 ? (
            <p className="empty">No spend yet.</p>
          ) : (
            <ul className="bars">
              {keys.map(([name, v]) => (
                <li key={name}>
                  <span className="bars-name">{name || "(no key)"}</span>
                  <span className="bars-track">
                    <span className="bars-fill" style={{ width: `${maxKey > 0 ? (v / maxKey) * 100 : 0}%` }} />
                  </span>
                  <span className="bars-value">{fmt.usd(v)}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
        <div className="card">
          <h2>Requests by model ({w})</h2>
          {models.length === 0 ? (
            <p className="empty">No traffic yet.</p>
          ) : (
            <table>
              <thead>
                <tr>
                  <th>Model alias</th>
                  <th className="num">Requests</th>
                </tr>
              </thead>
              <tbody>
                {models.map(([m, v]) => (
                  <tr key={m}>
                    <td>{m}</td>
                    <td className="num">{fmt.count(v)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </section>
  );
}
