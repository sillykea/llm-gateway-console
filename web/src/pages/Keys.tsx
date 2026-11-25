import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, ApiError, type Key, type Role } from "../api";
import { Dialog } from "../components/Dialog";
import { fmt } from "../format";

function message(e: unknown, fallback: string): string {
  return e instanceof ApiError ? e.message : fallback;
}

function toNumber(s: string): number {
  const n = Number(s);
  return Number.isFinite(n) ? n : NaN;
}

function BudgetBar({ spend, budget }: { spend: number | null; budget: number }) {
  if (spend === null) return <span className="muted">Unknown</span>;
  if (budget <= 0) return <span>{fmt.usd(spend)}</span>;
  const used = Math.min(spend / budget, 1);
  const tone = used >= 1 ? "bad" : used >= 0.8 ? "warn" : "ok";
  return (
    <span className="budget" title={`${fmt.pct(spend / budget)} of ${fmt.usd(budget)}`}>
      <span>
        {fmt.usd(spend)} <span className="muted">/ {fmt.usd(budget)}</span>
      </span>
      <span className="budget-track" role="progressbar" aria-valuenow={Math.round(used * 100)} aria-valuemin={0} aria-valuemax={100} aria-label={`Budget used for this key`}>
        <span className={`budget-fill budget-${tone}`} style={{ width: `${used * 100}%` }} />
      </span>
    </span>
  );
}

interface FormState {
  name: string;
  description: string;
  rpm: string;
  budget: string;
}

const emptyForm: FormState = { name: "", description: "", rpm: "60", budget: "50" };

export function Keys({ role }: { role: Role }) {
  const isAdmin = role === "admin";
  const [keys, setKeys] = useState<Key[] | null>(null);
  const [metricsOk, setMetricsOk] = useState(true);
  const [error, setError] = useState("");
  const [showRevoked, setShowRevoked] = useState(false);

  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState<FormState>(emptyForm);
  const [formError, setFormError] = useState("");
  const [busy, setBusy] = useState(false);
  const [secret, setSecret] = useState<{ name: string; value: string } | null>(null);
  const [copied, setCopied] = useState(false);

  const [editing, setEditing] = useState<Key | null>(null);
  const [edit, setEdit] = useState({ rpm: "", budget: "", description: "" });
  const [revoking, setRevoking] = useState<Key | null>(null);

  const load = useCallback(() => {
    api
      .keys()
      .then((r) => {
        setKeys(r.keys);
        setMetricsOk(r.metrics_available);
        setError("");
      })
      .catch((e: unknown) => setError(message(e, "Couldn't load keys.")));
  }, []);

  useEffect(load, [load]);

  async function create(e: FormEvent) {
    e.preventDefault();
    const rpm = toNumber(form.rpm);
    const budget = toNumber(form.budget);
    if (!Number.isInteger(rpm) || rpm < 0) return setFormError("Requests per minute must be a whole number, 0 for no limit.");
    if (Number.isNaN(budget) || budget < 0) return setFormError("Budget must be a number, 0 for no limit.");
    setBusy(true);
    setFormError("");
    try {
      const r = await api.createKey({ name: form.name.trim(), description: form.description.trim(), rpm, monthly_budget_usd: budget });
      setCreating(false);
      setForm(emptyForm);
      setCopied(false);
      setSecret({ name: r.key.name, value: r.secret });
      load();
    } catch (err) {
      setFormError(message(err, "Couldn't create the key."));
    } finally {
      setBusy(false);
    }
  }

  function startEdit(k: Key) {
    setEditing(k);
    setFormError("");
    setEdit({ rpm: String(k.rpm), budget: String(k.monthly_budget_usd), description: k.description });
  }

  async function saveEdit(e: FormEvent) {
    e.preventDefault();
    if (!editing) return;
    const rpm = toNumber(edit.rpm);
    const budget = toNumber(edit.budget);
    if (!Number.isInteger(rpm) || rpm < 0) return setFormError("Requests per minute must be a whole number, 0 for no limit.");
    if (Number.isNaN(budget) || budget < 0) return setFormError("Budget must be a number, 0 for no limit.");
    setBusy(true);
    try {
      await api.updateKey(editing.id, { rpm, monthly_budget_usd: budget, description: edit.description.trim() });
      setEditing(null);
      load();
    } catch (err) {
      setFormError(message(err, "Couldn't save the change."));
    } finally {
      setBusy(false);
    }
  }

  async function revoke() {
    if (!revoking) return;
    setBusy(true);
    try {
      await api.revokeKey(revoking.id);
      setRevoking(null);
      load();
    } catch (err) {
      setError(message(err, "Couldn't revoke the key."));
      setRevoking(null);
    } finally {
      setBusy(false);
    }
  }

  async function copySecret() {
    if (!secret) return;
    try {
      await navigator.clipboard.writeText(secret.value);
      setCopied(true);
    } catch {
      setCopied(false); // clipboard blocked: the key is still selectable on screen
    }
  }

  const visible = (keys ?? []).filter((k) => showRevoked || !k.revoked_at);
  const revokedCount = (keys ?? []).filter((k) => k.revoked_at).length;

  return (
    <section className="page">
      <header className="page-head">
        <h1>API keys</h1>
        {isAdmin && (
          <button
            className="primary"
            onClick={() => {
              setFormError("");
              setCreating(true);
            }}
          >
            New key
          </button>
        )}
      </header>

      {error && (
        <p className="banner banner-bad" role="alert">
          {error}
        </p>
      )}
      {!metricsOk && <p className="banner banner-warn">Spend is unavailable right now: Prometheus didn't answer.</p>}

      {keys && keys.length === 0 && (
        <div className="card empty-state">
          <p>No keys yet.</p>
          {isAdmin ? <p className="muted">Create one for each team or service that calls the gateway.</p> : <p className="muted">An admin can create keys.</p>}
        </div>
      )}

      {visible.length > 0 && (
        <div className="card table-wrap">
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Key</th>
                <th className="num">Rate limit</th>
                <th>Spend this month</th>
                <th>Created</th>
                <th>Status</th>
                {isAdmin && (
                  <th>
                    <span className="sr-only">Actions</span>
                  </th>
                )}
              </tr>
            </thead>
            <tbody>
              {visible.map((k) => (
                <tr key={k.id} className={k.revoked_at ? "row-revoked" : ""}>
                  <td>
                    <div className="key-name">{k.name}</div>
                    {k.description && <div className="muted small">{k.description}</div>}
                  </td>
                  <td>
                    <code>{k.prefix}…</code>
                  </td>
                  <td className="num">{fmt.limit(k.rpm, "/min")}</td>
                  <td>
                    <BudgetBar spend={k.spend_month_usd} budget={k.monthly_budget_usd} />
                  </td>
                  <td className="nowrap">
                    {fmt.day(k.created_at)} <span className="muted">by {k.created_by}</span>
                  </td>
                  <td>
                    {k.revoked_at ? (
                      <span className="badge badge-off" title={`Revoked by ${k.revoked_by ?? "?"} on ${fmt.dateTime(k.revoked_at)}`}>
                        Revoked
                      </span>
                    ) : (
                      <span className="badge badge-on">Active</span>
                    )}
                  </td>
                  {isAdmin && (
                    <td className="actions">
                      {!k.revoked_at && (
                        <>
                          <button onClick={() => startEdit(k)} aria-label={`Edit ${k.name}`}>
                            Edit
                          </button>
                          <button className="danger" onClick={() => setRevoking(k)} aria-label={`Revoke ${k.name}`}>
                            Revoke
                          </button>
                        </>
                      )}
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {revokedCount > 0 && (
        <label className="toggle">
          <input type="checkbox" checked={showRevoked} onChange={(e) => setShowRevoked(e.target.checked)} />
          Show {revokedCount} revoked {revokedCount === 1 ? "key" : "keys"}
        </label>
      )}

      <Dialog title="New API key" open={creating} onClose={() => setCreating(false)}>
        <form onSubmit={create} className="form">
          <label>
            Name
            <input
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value.toLowerCase() })}
              placeholder="team-search"
              pattern="[a-z0-9][a-z0-9\-]{1,39}"
              title="2–40 lowercase letters, digits or dashes"
              required
              autoFocus
            />
            <span className="muted small">Lowercase letters, digits and dashes. Names can't be reused.</span>
          </label>
          <label>
            Description <span className="muted">(optional)</span>
            <input value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} maxLength={200} />
          </label>
          <div className="row">
            <label>
              Requests per minute
              <input inputMode="numeric" value={form.rpm} onChange={(e) => setForm({ ...form, rpm: e.target.value })} required />
            </label>
            <label>
              Monthly budget (USD)
              <input inputMode="decimal" value={form.budget} onChange={(e) => setForm({ ...form, budget: e.target.value })} required />
            </label>
          </div>
          <p className="muted small">Use 0 for no limit.</p>
          {formError && (
            <p className="error" role="alert">
              {formError}
            </p>
          )}
          <div className="dialog-actions">
            <button type="button" onClick={() => setCreating(false)}>
              Cancel
            </button>
            <button className="primary" type="submit" disabled={busy}>
              {busy ? "Creating…" : "Create key"}
            </button>
          </div>
        </form>
      </Dialog>

      <Dialog title={`Key for ${secret?.name ?? ""}`} open={secret !== null} onClose={() => setSecret(null)}>
        <p className="banner banner-warn">Copy this key now. It won't be shown again: the console only keeps a hash of it.</p>
        <div className="secret">
          <code data-testid="new-secret">{secret?.value}</code>
          <button onClick={copySecret}>{copied ? "Copied" : "Copy"}</button>
        </div>
        <p className="muted small">Gateways pick up new keys within about 30 seconds.</p>
        <div className="dialog-actions">
          <button className="primary" onClick={() => setSecret(null)}>
            I've saved it
          </button>
        </div>
      </Dialog>

      <Dialog title={`Edit ${editing?.name ?? ""}`} open={editing !== null} onClose={() => setEditing(null)}>
        <form onSubmit={saveEdit} className="form">
          <label>
            Description
            <input value={edit.description} onChange={(e) => setEdit({ ...edit, description: e.target.value })} maxLength={200} />
          </label>
          <div className="row">
            <label>
              Requests per minute
              <input inputMode="numeric" value={edit.rpm} onChange={(e) => setEdit({ ...edit, rpm: e.target.value })} required />
            </label>
            <label>
              Monthly budget (USD)
              <input inputMode="decimal" value={edit.budget} onChange={(e) => setEdit({ ...edit, budget: e.target.value })} required />
            </label>
          </div>
          {formError && (
            <p className="error" role="alert">
              {formError}
            </p>
          )}
          <div className="dialog-actions">
            <button type="button" onClick={() => setEditing(null)}>
              Cancel
            </button>
            <button className="primary" type="submit" disabled={busy}>
              Save
            </button>
          </div>
        </form>
      </Dialog>

      <Dialog title={`Revoke ${revoking?.name ?? ""}?`} open={revoking !== null} onClose={() => setRevoking(null)}>
        <p>
          Anything using this key will start getting <code>401</code> errors within about 30 seconds. This can't be undone, and the name
          can't be reused.
        </p>
        <div className="dialog-actions">
          <button onClick={() => setRevoking(null)}>Cancel</button>
          <button className="danger-solid" onClick={revoke} disabled={busy}>
            Revoke key
          </button>
        </div>
      </Dialog>
    </section>
  );
}
