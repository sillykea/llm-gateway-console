// End-to-end tests: the real console binary, a fake Prometheus, headless
// Chromium, and (when GATEWAY_BIN is set) the real llm-gateway syncing keys
// from the console.
//
//   make e2e                                  console + browser only
//   GATEWAY_BIN=/path/to/gateway make e2e     also proves the gateway honours new and revoked keys

import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { spawn, execFileSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import http from "node:http";
import { tmpdir } from "node:os";
import path from "node:path";
import { chromium } from "playwright";

const root = path.resolve(import.meta.dirname, "..", "..");
const consoleBin = process.env.CONSOLE_BIN ?? path.join(root, "bin", "console");
const gatewayBin = process.env.GATEWAY_BIN;
const screenshots = process.env.SCREENSHOT_DIR;

const PUBLIC = 18090;
const INTERNAL = 18091;
const PROM = 18092;
const GATEWAY = 18093;
const base = `http://127.0.0.1:${PUBLIC}`;
const syncToken = "e2e-sync-token-e2e-sync-token-e2e-sync";

const procs = [];
let promServer;
let browser;

function hash(password) {
  return execFileSync(consoleBin, ["hash-password"], { input: password + "\n" }).toString().trim();
}

// A fake Prometheus with believable numbers, so the overview has something
// to draw (and the README screenshots look like a real deployment).
function startFakePrometheus() {
  const vector = (result) => ({ status: "success", data: { resultType: "vector", result } });
  const one = (v) => vector([{ metric: {}, value: [Date.now() / 1000, String(v)] }]);
  const answers = [
    ["sum by (key) (increase(llm_gateway_cost_usd_total", () =>
      vector([
        { metric: { key: "team-search" }, value: [0, "184.20"] },
        { metric: { key: "support-bot" }, value: [0, "96.75"] },
        { metric: { key: "batch-jobs" }, value: [0, "41.10"] },
      ])],
    ["sum by (alias) (increase(llm_gateway_requests_total", () =>
      vector([
        { metric: { alias: "chat-default" }, value: [0, "18450"] },
        { metric: { alias: "chat-private" }, value: [0, "3120"] },
      ])],
    ["llm_gateway_requests_total{status!=\"ok\"}", () => one(0.012)],
    ["max by (provider) (llm_gateway_breaker_open)", () => vector([])],
    ["histogram_quantile", () => one(1.84)],
    ["llm_gateway_cache_hits_total", () => one(0.31)],
    ["llm_gateway_fallbacks_total", () => one(57)],
    ["sum(increase(llm_gateway_cost_usd_total", () => one(322.05)],
    ["sum(increase(llm_gateway_requests_total", () => one(21570)],
  ];
  promServer = http.createServer((req, res) => {
    const url = new URL(req.url, "http://x");
    const q = url.searchParams.get("query") ?? "";
    res.setHeader("Content-Type", "application/json");
    if (url.pathname === "/api/v1/query_range") {
      const start = Number(url.searchParams.get("start"));
      const end = Number(url.searchParams.get("end"));
      const step = Number(url.searchParams.get("step"));
      const daily = step >= 86400;
      const values = [];
      for (let t = start, i = 0; t <= end; t += step, i++) {
        const v = daily ? 14 + 9 * Math.sin(i / 2) + (i % 3) * 2 : 600 + 450 * Math.sin((i - 6) / 3.8) + (i % 4) * 40;
        values.push([t, String(Math.max(0, v).toFixed(2))]);
      }
      res.end(JSON.stringify({ status: "success", data: { resultType: "matrix", result: [{ metric: {}, values }] } }));
      return;
    }
    const hit = answers.find(([sub]) => q.includes(sub));
    res.end(JSON.stringify(hit ? hit[1]() : vector([])));
  });
  return new Promise((r) => promServer.listen(PROM, "127.0.0.1", r));
}

function start(bin, args, env) {
  const p = spawn(bin, args, { env: { ...process.env, ...env }, stdio: ["ignore", "pipe", "pipe"] });
  let out = "";
  p.stdout.on("data", (d) => (out += d));
  p.stderr.on("data", (d) => (out += d));
  p.output = () => out;
  procs.push(p);
  return p;
}

async function waitFor(url, ok = (r) => r.ok, timeoutMs = 15000) {
  const until = Date.now() + timeoutMs;
  for (;;) {
    try {
      const r = await fetch(url);
      if (ok(r)) return r;
    } catch {}
    if (Date.now() > until) throw new Error(`timed out waiting for ${url}`);
    await new Promise((r) => setTimeout(r, 200));
  }
}

before(async () => {
  const dir = mkdtempSync(path.join(tmpdir(), "console-e2e-"));
  const usersFile = path.join(dir, "users.json");
  writeFileSync(
    usersFile,
    JSON.stringify([
      { username: "alice", role: "admin", password_hash: hash("admin password 1") },
      { username: "bob", role: "viewer", password_hash: hash("viewer password 1") },
    ]),
  );
  await startFakePrometheus();
  start(consoleBin, [], {
    CONSOLE_LISTEN: `127.0.0.1:${PUBLIC}`,
    CONSOLE_INTERNAL_LISTEN: `127.0.0.1:${INTERNAL}`,
    CONSOLE_DATA_DIR: path.join(dir, "data"),
    CONSOLE_USERS_FILE: usersFile,
    CONSOLE_SESSION_SECRET: "e2e-session-secret-e2e-session-secret",
    CONSOLE_SYNC_TOKEN: syncToken,
    CONSOLE_INSECURE_COOKIES: "true",
    PROMETHEUS_URL: `http://127.0.0.1:${PROM}`,
  });
  await waitFor(`${base}/healthz`);

  if (gatewayBin) {
    const cfg = path.join(dir, "gateway.json");
    writeFileSync(
      cfg,
      JSON.stringify({
        listen: `127.0.0.1:${GATEWAY}`,
        providers: [{ name: "local", type: "ollama", base_url: "http://127.0.0.1:9", local: true }],
        models: { "chat-default": [{ provider: "local", model: "llama3.1:8b" }] },
        cache: { backend: "off" },
        key_source: { url: `http://127.0.0.1:${INTERNAL}/internal/keys`, token_env: "SYNC_TOKEN", refresh_seconds: 5 },
      }),
    );
    start(gatewayBin, ["-config", cfg], { SYNC_TOKEN: syncToken });
    await waitFor(`http://127.0.0.1:${GATEWAY}/readyz`);
  }
  browser = await chromium.launch();
});

after(async () => {
  await browser?.close();
  for (const p of procs) p.kill("SIGTERM");
  promServer?.close();
});

async function signIn(page, user, password) {
  await page.goto(base);
  await page.getByLabel("Username").fill(user);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await page.getByRole("heading", { name: "Overview" }).waitFor();
}

function newPage() {
  return browser.newPage({ viewport: { width: 1320, height: 860 }, colorScheme: "light" });
}

describe("console", () => {
  test("wrong password is refused", async () => {
    const page = await newPage();
    await page.goto(base);
    await page.getByLabel("Username").fill("alice");
    await page.getByLabel("Password").fill("not the password");
    await page.getByRole("button", { name: "Sign in" }).click();
    await page.getByRole("alert").filter({ hasText: "wrong username or password" }).waitFor();
    await page.close();
  });

  test("viewer sees metrics but can't change keys or read the audit log", async () => {
    const page = await newPage();
    await signIn(page, "bob", "viewer password 1");
    await page.getByText("$322.05").waitFor();
    assert.equal(await page.getByText("team-search").count(), 1);
    assert.equal(await page.getByRole("link", { name: "Audit log" }).count(), 0);

    await page.getByRole("link", { name: "Keys" }).click();
    await page.getByRole("heading", { name: "API keys" }).waitFor();
    assert.equal(await page.getByRole("button", { name: "New key" }).count(), 0);

    // Going straight to the URL doesn't help: the page falls back and the API refuses.
    await page.goto(`${base}/audit`);
    await page.getByRole("heading", { name: "Overview" }).waitFor();
    const r = await page.request.get(`${base}/api/audit`);
    assert.equal(r.status(), 403);
    await page.close();
  });

  test("admin creates, edits and revokes a key, and the gateway follows", async () => {
    const page = await newPage();
    await signIn(page, "alice", "admin password 1");
    if (screenshots) await page.screenshot({ path: path.join(screenshots, "overview.png"), fullPage: true });

    await page.getByRole("link", { name: "Keys" }).click();
    let secret = "";
    for (const [name, rpm, budget] of [
      ["team-search", "120", "250"],
      ["support-bot", "60", "100"],
      ["batch-jobs", "0", "40"],
    ]) {
      await page.getByRole("button", { name: "New key" }).click();
      const dialog = page.getByRole("dialog", { name: "New API key" });
      await dialog.getByLabel("Name").fill(name);
      await dialog.getByLabel("Requests per minute").fill(rpm);
      await dialog.getByLabel("Monthly budget (USD)").fill(budget);
      await dialog.getByRole("button", { name: "Create key" }).click();
      await page.getByTestId("new-secret").waitFor();
      if (name === "team-search") {
        secret = await page.getByTestId("new-secret").innerText();
        assert.match(secret, /^lgw_[A-Za-z0-9_-]{43}$/);
        if (screenshots) await page.screenshot({ path: path.join(screenshots, "new-key.png") });
      }
      await page.getByRole("button", { name: "I've saved it" }).click();
    }

    // The table shows spend against budget; the secret is nowhere on the page.
    await page.getByRole("cell", { name: "team-search", exact: true }).waitFor();
    assert.equal(await page.getByText("$184.20").count(), 1);
    assert.equal(await page.getByText(secret).count(), 0);

    // A name can't be reused.
    await page.getByRole("button", { name: "New key" }).click();
    const dup = page.getByRole("dialog", { name: "New API key" });
    await dup.getByLabel("Name").fill("team-search");
    await dup.getByRole("button", { name: "Create key" }).click();
    await dup.getByRole("alert").filter({ hasText: "already exists" }).waitFor();
    await dup.getByRole("button", { name: "Cancel" }).click();

    // Edit a limit.
    await page.getByRole("button", { name: "Edit support-bot" }).click();
    const edit = page.getByRole("dialog", { name: "Edit support-bot" });
    await edit.getByLabel("Requests per minute").fill("90");
    await edit.getByRole("button", { name: "Save" }).click();
    await page.getByRole("cell", { name: "90 /min" }).waitFor();
    if (screenshots) await page.screenshot({ path: path.join(screenshots, "keys.png"), fullPage: true });

    const gateway = `http://127.0.0.1:${GATEWAY}/v1/models`;
    const auth = { headers: { Authorization: `Bearer ${secret}` } };
    if (gatewayBin) {
      // The gateway picks the new key up on its next sync.
      const r = await (async () => {
        const until = Date.now() + 15000;
        for (;;) {
          const res = await fetch(gateway, auth);
          if (res.status === 200 || Date.now() > until) return res;
          await new Promise((r) => setTimeout(r, 300));
        }
      })();
      assert.equal(r.status, 200, "gateway should accept the new console key");
    }

    // Revoke it.
    await page.getByRole("button", { name: "Revoke team-search" }).click();
    await page.getByRole("dialog", { name: "Revoke team-search?" }).getByRole("button", { name: "Revoke key" }).click();
    await page.getByRole("checkbox", { name: /Show 1 revoked key/ }).check();
    await page.getByRole("row", { name: /team-search/ }).getByText("Revoked").waitFor();

    if (gatewayBin) {
      const until = Date.now() + 15000;
      let status;
      do {
        status = (await fetch(gateway, auth)).status;
        if (status === 401) break;
        await new Promise((r) => setTimeout(r, 300));
      } while (Date.now() < until);
      assert.equal(status, 401, "gateway should reject the revoked key");
    }

    // Everything above is in the audit log.
    await page.getByRole("link", { name: "Audit log" }).click();
    await page.getByRole("cell", { name: "Revoked key" }).waitFor();
    assert.ok((await page.getByRole("cell", { name: "Created key" }).count()) >= 3);
    assert.equal(await page.getByRole("cell", { name: "Changed key" }).count(), 1);
    if (screenshots) await page.screenshot({ path: path.join(screenshots, "audit.png"), fullPage: true });

    // Signing out ends the session.
    await page.getByRole("button", { name: "Sign out" }).click();
    await page.getByRole("button", { name: "Sign in" }).waitFor();
    assert.equal((await page.request.get(`${base}/api/keys`)).status(), 401);
    await page.close();
  });

  test("no console errors and the CSP isn't violated", async () => {
    const page = await newPage();
    const problems = [];
    // The sign-in check before logging in is an expected 401; anything else
    // (a CSP violation, a React error, a failed asset) fails the test.
    page.on("console", (m) => m.type() === "error" && !m.text().includes("401 (Unauthorized)") && problems.push(m.text()));
    page.on("pageerror", (e) => problems.push(e.message));
    await signIn(page, "alice", "admin password 1");
    await page.getByRole("link", { name: "Keys" }).click();
    await page.getByRole("heading", { name: "API keys" }).waitFor();
    assert.deepEqual(problems, []);
    await page.close();
  });
});
