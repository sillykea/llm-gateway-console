"""Checks the running demo end to end: sign in to the console, then confirm
traffic, spend and cache hits made it from the gateway through Prometheus."""
import http.cookiejar
import json
import sys
import time
import urllib.request

BASE = "http://localhost:8090"
opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))


def call(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method,
                                 headers={"Content-Type": "application/json"} if data else {})
    with opener.open(req, timeout=10) as resp:
        return json.load(resp)


call("POST", "/api/login", {"username": "viewer", "password": "demo-viewer-password"})

deadline = time.time() + 180
while True:
    o = call("GET", "/api/overview")
    keys = call("GET", "/api/keys")
    problems = []
    if o["requests"] < 50:
        problems.append(f"only {o['requests']:.0f} requests so far")
    if o["spend_month_usd"] <= 0:
        problems.append("no spend yet")
    if o["cache_hit_rate"] <= 0:
        problems.append("no cache hits yet")
    names = sorted(k["name"] for k in keys["keys"])
    if names != ["batch-jobs", "support-bot", "team-search"]:
        problems.append(f"keys are {names}")
    elif not all((k["spend_month_usd"] or 0) > 0 for k in keys["keys"]):
        problems.append("not every key has spend yet")
    if not problems:
        break
    if time.time() > deadline:
        sys.exit("demo check failed: " + "; ".join(problems))
    print("waiting:", "; ".join(problems), flush=True)
    time.sleep(10)

print(f"requests: {o['requests']:.0f}, spend: ${o['spend_month_usd']:.2f}, "
      f"cache hit rate: {o['cache_hit_rate']:.0%}, fallbacks: {o['fallbacks']:.0f}")
for k in keys["keys"]:
    print(f"  {k['name']}: ${k['spend_month_usd']:.3f} of ${k['monthly_budget_usd']:.2f}")
print("demo OK")
