# 3. Stateless, signed session cookies

Status: accepted

## Context

Sessions need to survive a restart and work if the console ever runs more
than one replica, without adding a session store.

## Decision

The session cookie holds the username and an expiry, signed with
HMAC-SHA256 under `CONSOLE_SESSION_SECRET`. It is:

- `HttpOnly`, so script can't read it (an XSS can't steal it);
- `Secure` with the `__Host-` prefix, so it only travels over HTTPS and a
  subdomain can't set or overwrite it;
- `SameSite=Strict`, so the browser never sends it on cross-site requests;
- valid for 8 hours.

State-changing requests are also refused if `Origin` or `Sec-Fetch-Site`
shows they came from another site, and must have a JSON body, which a
plain HTML form can't send. The CSP allows only the app's own script.

## Consequences

- Signing out clears the cookie but can't invalidate a copy someone took.
  The 8-hour lifetime bounds that, and rotating the secret signs everyone
  out at once.
- Role changes still apply immediately (after a restart), because the role
  is looked up from the users file on each request, not stored in the cookie.
