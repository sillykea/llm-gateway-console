# 4. Keys and the audit log in files, for one replica

Status: accepted

## Context

The console stores key records (name, limits, SHA-256 of the key, who made
or revoked it) and an audit log. There are tens to a few thousand keys,
written a few times a day.

## Decision

- Keys are one JSON file, rewritten atomically on every change: write a
  temp file, fsync, rename over the old one, fsync the directory. A crash
  at any point leaves either the old file or the new one, never half of
  each.
- The audit log is append-only JSON lines, fsynced per event. A torn last
  line after a crash is skipped on read.
- Both live on a small persistent volume. The console runs as one replica.
- The store never sees a key, only its hash, so the file is not a list of
  usable credentials.

## Consequences

- One replica means a few seconds of console downtime during a deploy.
  Gateways don't notice: they keep the last key list they pulled (gateway
  ADR 6), so traffic is unaffected.
- No backups unless the volume has snapshots. Losing the file loses key
  records, not working keys: gateways keep serving their last list until
  the console comes back with a restored file.

## Moving to Postgres

When the console needs more than one replica, or the audit log needs
querying, move both to Postgres: a `keys` table with a unique name, and an
`audit_events` table. The store's methods (`Create`, `UpdateKey`, `Revoke`,
`ActiveKeys`, `Audit`) are the whole interface, and `Version` becomes a
sequence bumped in the same transaction as each write.
