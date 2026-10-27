# MailPit-Crew — Specification

Status: Draft v0.4 (phase 1, managed mode and phase 2 built)
Date: 2026-10-27
License (planned): MIT

> MailPit-Crew is an unofficial companion tool for [Mailpit](https://github.com/axllent/mailpit). It is not affiliated with the Mailpit project.

---

## 1. Overview

MailPit-Crew is a single Go binary that installs, configures and runs a stock Mailpit (managed mode), or manages a Mailpit you run yourself (external mode). It adds:

1. **Admin panel** — edit Mailpit configuration (settings, SMTP accounts, tag rules) through validated web forms, then apply changes and restart Mailpit safely.
2. **Per-user inbox** — users log in to MailPit-Crew and see only the messages they own, based on ownership rules.
3. **Per-user POP3** (later phase) — a POP3 server that applies the same ownership rules.

Mailpit is never modified. MailPit-Crew only uses Mailpit's public HTTP API and its documented configuration files.

### 1.1 Goals

- No fork, no patch to Mailpit.
- No manual YAML / htpasswd / env-file editing for common tasks.
- Safe apply: validate, back up, write atomically, restart, health check, roll back on failure.
- Per-user mail visibility for a shared Mailpit instance.
- One binary. Works on bare metal, with systemd, or in Docker.
- Configurable like Mailpit: flags, env vars, optional config file.

### 1.2 Non-goals

- Storing mail. All mail stays in Mailpit.
- Hard multi-tenant isolation for hostile users. Target is internal / home-lab mail testing.
- Real mail delivery (MTA features).
- Replacing every Mailpit feature in the user UI (screenshots, link check, spam check, etc. are out of scope for v1).

### 1.3 Terms

| Term | Meaning |
|---|---|
| Crew | The MailPit-Crew process. |
| Crew user | A person who logs in to Crew (admin or user role). Stored in Crew's users file. |
| SMTP account | A Mailpit SMTP login (in Mailpit's `smtp.auth`). Usually one per sending app. |
| Ownership rule | A rule that says which messages a Crew user may see. |
| Managed file | A Mailpit file that Crew reads and writes (env file, auth files, tags file). |
| Apply | The process that writes managed files and restarts Mailpit. |

---

## 2. Architecture

```
                     ┌──────────────────────────────────────────┐
  Browser  ──HTTPS──▶│ MailPit-Crew  :8025                      │
                     │  - Admin panel                           │
                     │  - User inbox UI                         │
                     │  - Ownership filter                      │
  POP3 client ─TLS──▶│  - POP3 server :1110 (phase 3)           │
                     └───────────────┬──────────────────────────┘
                                     │ HTTP (Mailpit API)
                                     ▼  127.0.0.1:8026
  Apps (gitea, ...) ──SMTP/TLS──▶ ┌────────────────────────┐
                        :1025     │ Mailpit (stock)        │
                                  │  SQLite DB             │
                                  └────────────────────────┘
                     Crew writes: mailpit.ini, smtp.auth, pop3.auth,
                                  tags.yaml, (relay config)
```

- Mailpit UI/API binds to localhost only (for example `127.0.0.1:8026`). Users never reach it directly.
- Mailpit SMTP stays public. Sending apps need no change.
- Crew terminates TLS for the web UI and POP3, reusing the existing certificates.
- Crew calls the Mailpit API with a dedicated service account.

### 2.1 Required Mailpit settings

Crew checks these at startup and in the admin status page. It warns (or offers to fix) when one is missing.

| Setting | Required value | Reason |
|---|---|---|
| `MP_DATABASE` | set to a file | Restart must not lose mail. |
| `MP_UI_BIND_ADDR` | localhost address | Users must go through Crew. |
| `MP_TAGS_USERNAME` | `true` | Messages get tagged with the SMTP username (ownership). |
| `MP_UI_AUTH_FILE` | file containing Crew's service account | Defense in depth on the API. |
| `MP_POP3_AUTH_FILE` | separate file (admin only) until phase 3 | Mailpit POP3 shows all mail. |

---

## 3. Modes

Crew has two modes (`mode`):

| Mode | Who runs Mailpit | How Crew restarts it |
|---|---|---|
| `managed` (default) | Crew, through `mailpit/ctl.sh` | `ctl.sh restart` |
| `external` | You (systemd unit, container, script) | `restart.mode`: `command` or `child` |

### 3.1 Managed mode

Only `mailpit-crew` is run. Crew owns the folder layout under `home`:

```
<home>/data/      users.yaml, state.yaml (API password), backups/, audit.log
<home>/mailpit/   ctl.sh, bin/, bin/current, mailpit.ini, ui.auth, smtp.auth,
                  pop3.auth, send-api.auth, tags.yaml, mailpit.db, mailpit.log,
                  mailpit.pid, ssl/
```

**First start**
1. Create the folders, `ctl.sh`, `tags.yaml` (`filters: []`) and `mailpit.ini` with the locked settings (3.3).
2. Generate the Mailpit API service account (`crew`, random password in `data/state.yaml`, mode 0600) and write it to `ui.auth`.
3. Choose the Mailpit version, in this order:
   - `managed.version` (`latest`, a tag, or a binary path), for headless installs;
   - a terminal prompt when stdin is a terminal: 1) latest, 2) latest-1, 3) latest-2, 4) other (tag or binary path), Enter = skip;
   - otherwise the web UI shows "Mailpit is not set up" and the **Mailpit** page.

**Every start**: repair locked settings in `mailpit.ini`, repair the service account, run `ctl.sh start` (no-op when Mailpit is already running), start the watchdog.

**Versions**
- Release list: GitHub API `repos/axllent/mailpit/releases` (stable only, sorted by version), fallback to the releases Atom feed, cached 15 minutes.
- Download: `releases/download/<tag>/mailpit-<os>-<arch>.tar.gz`, extract `mailpit`, run `mailpit version` and require it to report the tag, then store as `bin/mailpit-<tag>`. Releases have no checksums.
- Custom binary (option 4): copied into `bin/` as `mailpit-<version>` (or `mailpit-custom-<time>` when the version is not a release tag).
- Switch: point `bin/current` at the new binary, restart, health check (same as apply). On failure, point it back and restart again. A failed first version leaves no version selected.
- Installed binaries are listed; inactive ones can be removed.

**Watchdog**: every 30 seconds, if a version is selected and `ctl.sh status` says not running, run `ctl.sh start` (skipped while an apply or switch runs).

**Crew shutdown**: Mailpit keeps running. The systemd unit uses `KillMode=process` so Crew restarts do not stop Mailpit.

### 3.2 ctl.sh

Generated by Crew, can be run and edited by hand. Commands: `start`, `stop`, `restart`, `status` (exit 0 = running, 3 = not running), `version`.
- Reads `mailpit.ini` line by line and exports values without `eval`, so values are never run as shell code.
- Runs `bin/current` with `nohup`, output to `mailpit.log` (rotated to `mailpit.log.1` above 10 MB), pid in `mailpit.pid`.
- `status` treats a zombie process as not running (checked in `/proc`).
- `stop`: `SIGTERM`, wait up to 10 seconds, then `SIGKILL`.
- Template has a version line (`# mailpit-crew-ctl: N`). When Crew ships a newer template it replaces the script and keeps the old one as `ctl.sh.old`.
- Crew runs it without `MP_*` variables from its own environment.

### 3.3 Locked settings (managed mode)

| Setting | Value |
|---|---|
| `MP_DATABASE` | `<home>/mailpit/mailpit.db` |
| `MP_UI_BIND_ADDR` | `managed.api_listen` (loopback only) |
| `MP_UI_AUTH_FILE` | `<home>/mailpit/ui.auth` |
| `MP_TAGS_USERNAME` | `true` |
| `MP_UI_AUTH`, `MP_UI_TLS_CERT`, `MP_UI_TLS_KEY`, `MP_WEBROOT`, `MP_LOG_FILE` | not set |

Shown read-only in the settings form, ignored on form submit, rejected by validation in the raw editor, and reset on start.

Optional password files (`MP_SMTP_AUTH_FILE`, `MP_POP3_AUTH_FILE`, `MP_SEND_API_AUTH_FILE`) are turned on with an **Enable** button that sets the key to `<home>/mailpit/<name>.auth`.

### 3.4 Import

`mailpit-crew import <old mailpit.ini>` copies an existing setup into the managed layout (only when `mailpit.db` does not exist yet): database with `-wal`/`-shm`, password files, tag rules, TLS and other referenced files (keys mode 0600), and all other `MP_*` values. Locked settings are skipped. Web UI accounts with bcrypt hashes become Crew admin logins with the same password.

### 3.5 External mode

**`command`**: Crew runs a user-defined command (no shell; argument list), for example `systemctl restart mailpit`. Exit code 0 = restart accepted, then the health check.

**`child`**: Crew starts Mailpit as a child process with the env file, restarts it with back-off when it exits, and stops it on shutdown.

Health check (all modes): poll `readyz`, then wait 5 seconds and check Mailpit's uptime (7, step 6).

---

## 4. Configuration

### 4.1 Sources and priority

1. Command-line flags
2. Environment variables (`MPC_` prefix)
3. Config file (`--config /any/path/config.yaml`, env `MPC_CONFIG`)
4. Built-in defaults

The config file is optional. Every setting is available as a flag and an env var.

### 4.2 Settings

| Key (YAML) | Flag | Env | Default | Description |
|---|---|---|---|---|
| `listen` | `--listen` | `MPC_LISTEN` | `[::]:8025` | Crew web UI bind address. |
| `webroot` | `--webroot` | `MPC_WEBROOT` | `/` | URL prefix. |
| `tls.cert` | `--tls-cert` | `MPC_TLS_CERT` | — | TLS cert for web UI and POP3. |
| `tls.key` | `--tls-key` | `MPC_TLS_KEY` | — | TLS key. |
| `data_dir` | `--data-dir` | `MPC_DATA_DIR` | `./data` | Crew state: users file, backups, audit log. |
| `users_file` | `--users-file` | `MPC_USERS_FILE` | `<data_dir>/users.yaml` | Crew users and ownership rules. |
| `log_file` | `--log-file` | `MPC_LOG_FILE` | stdout | Crew log output. |
| `log_level` | `--log-level` | `MPC_LOG_LEVEL` | `info` | `error`, `warn`, `info`, `debug`. |
| `mailpit.api_url` | `--mailpit-api` | `MPC_MAILPIT_API` | `http://127.0.0.1:8026` | Mailpit API base URL. |
| `mailpit.api_user` | `--mailpit-api-user` | `MPC_MAILPIT_API_USER` | — | Service account username. |
| `mailpit.api_password` | `--mailpit-api-password` | `MPC_MAILPIT_API_PASSWORD` | — | Service account password. |
| `mailpit.env_file` | `--mailpit-env-file` | `MPC_MAILPIT_ENV_FILE` | — | Mailpit env file (for example `mailpit.ini`). |
| `mailpit.smtp_auth_file` | ... | ... | read from env file | Override path. |
| `mailpit.ui_auth_file` | ... | ... | read from env file | Override path. |
| `mailpit.pop3_auth_file` | ... | ... | read from env file | Override path. |
| `mailpit.tags_file` | ... | ... | read from env file | Override path. |
| `mailpit.log_file` | ... | ... | read from env file | Mailpit log, shown after a failed apply. |
| `mailpit.managed_dirs` | `--mailpit-managed-dirs` | `MPC_MAILPIT_MANAGED_DIRS` | env file directory | Crew only writes files inside these directories. |
| `mailpit.relay_file` | ... | ... | read from env file | Override path (phase 3). |
| `restart.mode` | `--restart-mode` | `MPC_RESTART_MODE` | `command` | `child` or `command`. |
| `restart.command` | `--restart-command` | `MPC_RESTART_COMMAND` | — | Argument list for `command` mode. |
| `restart.timeout` | `--restart-timeout` | `MPC_RESTART_TIMEOUT` | `30s` | Restart + health check timeout. |
| `child.binary` | `--mailpit-binary` | `MPC_MAILPIT_BINARY` | `mailpit` | Mailpit binary for `child` mode. |
| `child.args` | `--mailpit-args` | `MPC_MAILPIT_ARGS` | — | Extra arguments for `child` mode. |
| `backups.keep` | `--backups-keep` | `MPC_BACKUPS_KEEP` | `20` | Number of apply backups to keep. |
| `session.ttl` | `--session-ttl` | `MPC_SESSION_TTL` | `12h` | Login session lifetime. |
| `pop3.listen` | `--pop3-listen` | `MPC_POP3_LISTEN` | disabled | Crew POP3 server (phase 3). |

Managed file paths are discovered from the Mailpit env file first (for example `MP_SMTP_AUTH_FILE`). Explicit Crew settings override them.

### 4.3 Example config (author's setup)

```yaml
listen: "[::]:8025"
tls:
  cert: /opt/mailpit/ssl/mail.mamber.in.fullchain.pem
  key: /opt/mailpit/ssl/mail.mamber.in.key.pem
data_dir: /opt/mailpit-crew/data
mailpit:
  api_url: http://127.0.0.1:8026
  api_user: crew
  api_password: "<secret>"
  env_file: /opt/mailpit/mailpit.ini
restart:
  mode: command
  command: ["systemctl", "restart", "mailpit"]
```

---

## 5. Crew users and ownership

### 5.1 Users file

YAML, owned by Crew, written only through the admin panel (or by hand while Crew is stopped).

```yaml
users:
  - username: mamber
    password: "$2a$10$..."        # bcrypt
    role: admin                   # admin | user
    enabled: true
  - username: dev1
    password: "$2a$10$..."
    role: user
    enabled: true
    rules:
      - type: smtp_user           # message tag equals SMTP username
        value: gitea
      - type: from                # sender address, glob
        value: "*@forge.mamber.in"
      - type: to                  # To/Cc/Bcc address, glob
        value: "qa+*@mamber.in"
      - type: tag                 # any Mailpit tag
        value: Vaultwarden
```

### 5.2 Rule types

| Type | Matches when |
|---|---|
| `smtp_user` | The message's `Username` (the SMTP login, stored by Mailpit) equals the value. If Mailpit stored no username, a tag with that name also matches (`MP_TAGS_USERNAME`). Case-insensitive. Needs Mailpit v1.28.3+ for `AUTH PLAIN`. |
| `from` | Sender address matches the glob. |
| `to` | Any To, Cc or Bcc address matches the glob. |
| `tag` | Message has the tag. Case-insensitive. |

- Rules are OR'ed: a message is visible when any rule matches.
- `admin` role sees all messages; rules are ignored.
- A user with no rules sees nothing.
- Globs: `*` = any characters, `?` = one character. Matching is case-insensitive.

### 5.3 Evaluation strategy

Crew evaluates rules **in Go**, on message summaries from the Mailpit API (From, To, Cc, Bcc, Tags). It does not depend on Mailpit search syntax supporting OR.

- **Single message access** (view, raw, attachment, delete): fetch the summary, evaluate rules, return `404` when not owned (do not reveal existence).
- **List**: page through Mailpit's message list, filter in Crew, paginate the filtered result. A short-lived cache (for example 5 seconds) avoids repeated full scans.
- **Search**: pass the user's search text to Mailpit search, then filter the results with the user's rules.
- Scale target: up to roughly 10 000 stored messages. Larger instances are out of scope for v1.

---

## 6. Managed Mailpit files

General rules for every managed file:

- Parse, validate, and write back **preserving** comments, order and unknown keys where the format allows.
- Write atomically: temp file in the same directory, `fsync`, rename.
- Keep file mode and owner.
- Detect external changes (hash at load time); refuse to overwrite and show a conflict if the file changed on disk.
- Only write files inside `mailpit.managed_dirs`. Files outside are read-only in the UI.
- File paths in Mailpit settings must be absolute. Mailpit resolves relative paths from its working directory (`/` under systemd), so Crew rejects them.

### 6.1 Env file (`mailpit.ini`)

- Format: `KEY=value` lines, optional quotes, `#` comments (systemd `EnvironmentFile` compatible).
- The admin form shows **known** `MP_*` keys grouped by section (General, Storage, Web UI, SMTP, POP3, Tags, Relay, Webhook).
- Each key has a type and validator: string, int, bool, duration (`30d`, `12h`), file path (must exist and be readable), address (`host:port`), regex, cert/key pair (must parse and match).
- Unknown keys are kept and shown read-only in an "Other" section, with a raw edit option.
- Known-key list is versioned per Mailpit release; Crew reads `mailpit version` to pick it.

### 6.2 Auth files (`smtp.auth`, `users.auth`, `pop3.auth`)

- Format: htpasswd (`user:hash` per line).
- Admin can add, remove, rename, reset password, enable/disable.
- New passwords: bcrypt (cost 10). Crew can generate a random password and show it once.
- Existing hashes of other supported formats are kept untouched.
- Warnings: duplicate passwords across accounts, empty file in use, username typos are not detectable but recent failed logins are shown (see 8.4).
- The Crew service account in `users.auth` is protected from deletion.

### 6.3 Tags file (`tags.yaml`)

- Schema: `filters:` list of `{ match: <search query>, tags: <comma list> }`.
- Form editor: add, remove, reorder rules.
- Validation: `filters` is a list; each item has non-empty `match` and `tags`. (Prevents errors like `missing tag: array`.)
- Optional "test rule" button: runs `match` as a Mailpit search and shows how many messages match.
- Commented-out example blocks are preserved.

### 6.4 Relay file (phase 3)

- Form for SMTP relay config: host, port, TLS mode, auth, allowed/blocked recipients.
- "Test connection" button.

---

## 7. Apply flow

All admin changes are staged first. Nothing touches disk until **Apply**.

1. **Validate** all staged changes. Stop on any error.
2. **Diff preview**: show per-file changes (passwords masked).
3. **Backup**: copy every managed file to `<data_dir>/backups/<timestamp>/`.
4. **Write** changed files atomically.
5. **Restart** Mailpit (restart mode).
6. **Health check** Mailpit `readyz` within `restart.timeout`, then wait 5 seconds and check Mailpit's uptime. Mailpit can report ready and exit a moment later (for example when the SMTP port is taken), so it must stay up.
7. **On failure**: restore the backup, restart again, report the error and the last Mailpit log lines.
8. **Audit**: record who applied what (see 9.4).

- Only one apply runs at a time (lock).
- Changes that need no Mailpit restart (Crew users and rules) apply immediately, without steps 5–6.
- Admin can restore any backup from the backups page (same flow from step 4).

---

## 8. Web UI

Server-rendered Go templates with a small amount of plain JavaScript. All assets embedded in the binary (`embed`). No frontend build step.

### 8.1 Login

- Username + password form. Session cookie.
- Disabled users cannot log in.

### 8.2 User inbox (all roles)

- Message list: from, to, subject, date, tags, read state, size, attachment icon.
- Pagination, search box, tag filter.
- Message view tabs: HTML, Text, Headers, Raw, Attachments.
- HTML view in a sandboxed `iframe` (no scripts), remote content blocked by default with a "load remote content" toggle.
- Actions: mark read/unread, delete, download `.eml`, download attachment.
- Auto refresh (polling every N seconds) for new messages.
- Users see only owned messages. Admin sees all.

### 8.3 Admin panel (admin role)

- **Dashboard / Status**: Mailpit version, uptime, message count, Crew version, required-settings check (2.1), last apply result.
- **Settings**: env file form (6.1).
- **SMTP accounts**: `smtp.auth` editor (6.2).
- **UI / POP3 accounts**: `users.auth`, `pop3.auth` editors (6.2).
- **Tag rules**: `tags.yaml` editor (6.3).
- **Crew users**: add/edit/disable users, set role, edit ownership rules, "preview visible messages" for a user.
- **Pending changes**: diff + Apply / Discard (7).
- **Backups**: list, view, restore.
- **Logs**: tail Mailpit log (if `MP_LOG_FILE` set or `child` mode) and Crew log; highlight failed SMTP logins.
- **Audit log**.

### 8.4 Failed login insight

Crew parses Mailpit log lines like `deny PLAIN login:"<user>"` and shows recent failed logins with source IP. Unknown usernames close to an existing account are flagged (for example `vautlwarden` vs `vaultwarden`).

---

## 9. Security

### 9.1 Authentication

- Crew users: bcrypt password hashes in the users file.
- Sessions: random 256-bit ID, server-side store (memory), cookie `HttpOnly`, `Secure` (when TLS), `SameSite=Strict`. Crew restart logs everyone out.
- Login rate limit per IP and per username.

### 9.2 Authorization

- Every message route checks ownership (5.3). Not owned = `404`.
- Admin routes require `admin` role.
- CSRF token on every state-changing request.

### 9.3 Hardening

- Security headers: CSP, `X-Frame-Options`, `X-Content-Type-Options`, `Referrer-Policy`.
- Email HTML never runs in the Crew origin context (sandboxed `iframe`, strict CSP).
- Restart command runs without a shell, from config only. It is not editable from the web UI.
- Secrets (service account password) are never shown in the UI or logs.
- Managed files keep their original permissions; users file written `0600`.
- Docker socket mode is documented as root-equivalent access.

### 9.4 Audit log

Append-only JSON lines in `<data_dir>/audit.log`: time, Crew user, IP, action, target, result. Passwords never logged.

---

## 10. POP3

### Phase 1–2: admin only

- Mailpit POP3 uses a dedicated `pop3.auth` with admin accounts only.
- Crew warns if POP3 shares a file with other accounts.

### Phase 3: Crew POP3 server

- POP3 (RFC 1939) server inside Crew, with `STLS` and implicit TLS.
- Login with Crew user credentials.
- Mailbox = the user's owned messages (snapshot at login).
- `RETR` returns raw message from Mailpit API. `DELE` deletes only owned messages, applied at `QUIT`.
- Implementation: write a small server, or reuse Mailpit's MIT-licensed POP3 code with attribution. Decide during phase 3.
- When enabled, Mailpit's own POP3 is disabled and Crew takes the POP3 port.

---

## 11. Mailpit API usage

Crew uses only the public API. Endpoint list must be verified against Mailpit v1.28 and v1.32 swagger docs.

| Purpose | Mailpit endpoint (expected) |
|---|---|
| Health | `GET /readyz` |
| Info / version | `GET /api/v1/info` |
| List | `GET /api/v1/messages?start=&limit=` |
| Search | `GET /api/v1/search?query=&start=&limit=` |
| Message | `GET /api/v1/message/{ID}` |
| Headers | `GET /api/v1/message/{ID}/headers` |
| Raw | `GET /api/v1/message/{ID}/raw` |
| Attachment | `GET /api/v1/message/{ID}/part/{PartID}` |
| Read state | `PUT /api/v1/messages` |
| Delete | `DELETE /api/v1/messages` (with IDs only — never empty body) |
| Tags | `GET /api/v1/tags` |

Rule: Crew never sends a delete request without explicit message IDs (an empty body deletes all mail in Mailpit).

---

## 12. Project layout (planned)

```
mailpit-crew/
  cmd/mailpit-crew/        main, flags
  internal/config/         flags, env, file, defaults
  internal/mailpit/        API client, version detection
  internal/files/          env file, htpasswd, tags, relay parsers/writers
  internal/apply/          staging, diff, backup, write, rollback
  internal/restart/        child and command modes, health check
  internal/users/          users file, rules, ownership evaluation
  internal/web/            handlers, sessions, CSRF, templates
  internal/pop3/           phase 3
  internal/web/assets/      templates, CSS, JS (embedded)
  deploy/systemd/          mailpit-crew.service example
  deploy/docker/           Dockerfile, docker-compose.yml
  SPEC.md  README.md  LICENSE
```

---

## 13. Testing

- Unit tests: parsers/writers (round-trip keeps comments and unknown keys), validators, rule evaluation, config priority.
- Integration tests: start a real Mailpit binary (v1.28 and latest) in `child` mode, send mail via SMTP with different usernames, check per-user visibility, apply + rollback.
- Security tests: access to non-owned message IDs returns `404`; CSRF missing = rejected; non-admin blocked from admin routes.
- CI: GitHub Actions, Linux amd64/arm64 builds.

---

## 14. Packaging and docs

- Release binaries: linux amd64/arm64 (others if trivial).
- `deploy/systemd/mailpit-crew.service` example.
- Docker image and `docker-compose.yml` (after binary works):
  - Single-container option: Crew in `child` mode, Mailpit bundled.
  - Two-container option: documented with `command` mode.
- README sections: bare metal, systemd, Docker; migration guide from an existing Mailpit setup (move UI to localhost, enable `MP_TAGS_USERNAME`, split POP3 auth file).

---

## 15. Phases and acceptance criteria

### Phase 1 — Admin panel
- Config loading (flags, env, file).
- Mailpit API client, status page.
- Editors: env file, auth files, tags file.
- Apply flow with backup, restart (`child` and `command`), health check, rollback.
- Crew admin login.

Done when: author's setup at `/opt/mailpit` is fully managed from the UI; a bad change rolls back automatically; no manual file edits needed.

### Phase 2 — Per-user inbox
- Crew users and ownership rules.
- User inbox UI (list, search, view, delete, read state, downloads).
- Ownership checks on every route.

Done when: a `user` sees only owned mail through every route; admin sees all.

### Phase 3 — Extras
- Crew POP3 server.
- Relay config editor.
- Failed-login insight, audit log viewer, backup restore UI.
- Docker image + compose, README for all modes.

Done when: POP3 clients see only owned mail; Docker compose starts a working stack.

---

## 16. Verification items

Checked against Mailpit v1.28.0 (source and a running binary):

1. **Username tag case**: the tag is the SMTP username as typed (title case is not applied). If a tag with the same name in another case already exists (for example `Gitea` from `tags.yaml`), Mailpit reuses that tag. Ownership matching must ignore case.
2. **Bcc**: envelope recipients that are not in To/Cc are added to the stored `Bcc` header, so they appear in message summaries.
3. **API**: endpoints in section 11 exist in v1.28. v1.32 still to check.
4. **Reload**: auth files and `tags.yaml` are read only at startup. Every change needs a restart.
5. **Search OR**: not needed; Crew evaluates rules itself.
6. **Env keys**: the UI listen key is `MP_UI_BIND_ADDR` and SMTP is `MP_SMTP_BIND_ADDR` (not `MP_LISTEN`). Bool values are true for `1`, `true` or `yes`. The catalog is in `internal/catalog`.
7. **Empty tags file**: Mailpit accepts `filters: []`, but fails on a file without a `filters` key (`missing tag: array`).
8. **Ready then crash**: Mailpit answers `readyz` before the SMTP listener binds. A taken SMTP port makes it exit about 300 ms later. The apply flow handles this (section 7, step 6).
9. **SMTP username**: message summaries have a `Username` field. Mailpit v1.28.0-v1.28.2 set it only for `AUTH LOGIN`; v1.28.3 fixed `AUTH PLAIN` ("Auto-tagging using SMTP username using plain auth", #617). Crew warns for older versions.
10. **Dangerous API defaults**: `DELETE /api/v1/messages` with no IDs (or an unreadable body) deletes **all** messages, and `PUT /api/v1/messages` with no IDs marks all messages read. The ID `latest` means the newest message. Crew validates IDs, never sends an empty list, and rejects `latest`.
11. **Read state**: `GET /api/v1/message/{id}` does not mark a message read. Crew marks it read with `PUT` after the ownership check.
12. **Upgrade**: switching a live install from v1.28.0 to v1.32.0 through the Mailpit page kept the database and mail.

## 17. Open questions

1. Naming: confirm with the Mailpit author that "MailPit-Crew" is acceptable before publishing.
2. Minimum supported Mailpit version (proposal: v1.28).
3. Crew default port when installed next to an existing Mailpit (proposal: take `8025`, move Mailpit to `127.0.0.1:8026`).
