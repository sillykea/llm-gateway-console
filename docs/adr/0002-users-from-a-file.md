# 2. Users come from a file, not a sign-up flow

Status: accepted

## Context

The console needs to know who can see spend (viewers) and who can create
and revoke keys (admins). The people involved are a platform team and a few
team leads, not the public.

## Decision

Users and roles are a JSON file with PBKDF2-SHA256 password hashes
(600,000 iterations, the OWASP figure for this algorithm), mounted from a
Kubernetes Secret. `console hash-password` produces the hashes. There is no
sign-up, no password reset and no user-management UI.

Logins are rate-limited per client and per username (5 failures per 15
minutes), take the same time whether or not the user exists, and are
written to the audit log either way.

## Consequences

- Adding or removing someone is a change to the Secret and a restart, which
  goes through the same review as any other config change.
- Sessions are checked against the file on every request, so a removed
  user is locked out on the next restart, not when their cookie expires.
- No password reset means no reset tokens and no email to attack.

## Later

For more than a handful of users, replace the file with OIDC sign-in (the
company's identity provider) and map groups to roles. The rest of the
console only sees a username and a role, so that change stays inside
`internal/auth`.
