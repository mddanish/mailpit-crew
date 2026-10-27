# MailPit-Crew — Specification

Status: v0.5 (managed Mailpit with config editors)
Date: 2026-10-27
License: MIT

> MailPit-Crew is an unofficial companion tool for [Mailpit](https://github.com/axllent/mailpit). It is not affiliated with the Mailpit project.

---

## 1. Overview

MailPit-Crew is one Go binary that installs, configures and runs a stock Mailpit. Mailpit is a mail testing tool, not a mail server, so users read mail in Mailpit's own web UI. Crew does not show mail; it manages Mailpit fully:

- Mailpit versions (download, switch, roll back),
- the Mailpit process (through `ctl.sh`),
- every Mailpit file: settings, password files, tag rules, SMTP relay and forward.

### 1.1 Goals

- No fork, no patch to Mailpit.
- No manual editing of env, htpasswd or YAML files.
- Every file verified the way Mailpit reads it, with line numbers.
- Changes saved with a backup; Mailpit restarted only when the admin chooses.
- Clear notice when saved files are not active yet; a failed restart offers fix or rollback.
- Configurable like Mailpit: flags, env vars, optional config file.

### 1.2 Non-goals

- Showing or storing mail (Mailpit's UI does that).
- Per-user mailboxes or a POP3 proxy.
- Managing a Mailpit that is run outside Crew.

---

## 2. Architecture

```
Admin browser ──▶ MailPit-Crew :8080 ──▶ mailpit/ctl.sh ──▶ Mailpit
                       │                                     ▲
                       └── Mailpit API (MP_UI_BIND_ADDR) ────┘
Users' browsers ──▶ Mailpit web UI :8025 (read mail)
Apps ──SMTP──▶ Mailpit :1025
```

- Crew writes Mailpit's files under `<home>/mailpit` and only there.
- Crew reaches the API through the web UI address in the settings (wildcard host becomes `127.0.0.1`; `https` when `MP_UI_TLS_CERT` is set, without name check). The client follows the address after each restart.
- Crew's API account (`crew`, random password in `data/state.yaml`) lives in `ui.auth`, hidden in the UI and protected from changes.
- One Crew login (`admin`). `mailpit-crew reset-password` sets a new one from the command line.

### 2.1 Folder layout

```
<home>/data/      users.yaml, state.yaml, active/, backups/, audit.log
<home>/mailpit/   ctl.sh, bin/ (releases, current symlink), mailpit.ini,
                  ui.auth, smtp.auth, pop3.auth, send-api.auth,
                  tags.yaml, relay.yaml, forward.yaml, ssl/,
                  mailpit.db, mailpit.log, mailpit.pid
```

### 2.2 Locked settings

| Setting | Value |
|---|---|
| `MP_DATABASE` | `<home>/mailpit/mailpit.db` |
| `MP_UI_AUTH_FILE` | `<home>/mailpit/ui.auth` |
| `MP_UI_AUTH`, `MP_WEBROOT`, `MP_LOG_FILE` | not set |

Shown read-only, rejected by validation, reset on start. All other `MP_*` settings are editable, including the web UI address and TLS.

---

## 3. Mailpit versions and process

- **First start**: create folders, `ctl.sh`, `tags.yaml` (`filters: []`), `mailpit.ini` (`MP_UI_BIND_ADDR=[::]:8025`, `MP_SMTP_BIND_ADDR=[::]:1025`, locked keys, tags file), service account.
- **Version choice**: `managed.version`, else a terminal prompt (1 latest, 2 latest-1, 3 latest-2, 4 other: tag or binary path), else the Mailpit page.
- **Releases**: GitHub API (stable only, by version), Atom feed fallback, cached 15 minutes. Download `mailpit-<os>-<arch>.tar.gz`, extract `mailpit`, require `mailpit version` to report the tag. Custom binaries are copied into `bin/`.
- **Switch**: point `bin/current` at the new binary, restart, health check; on failure point back and restart.
- **ctl.sh**: `start | stop | restart | status | version`; reads `mailpit.ini` without `eval`; output to `mailpit.log` (rotated at 10 MB); zombies count as stopped; newer templates replace it and keep `ctl.sh.old`.
- **Watchdog**: every 30 s, start Mailpit if it stopped, except after a failed restart (the admin decides).
- **Crew shutdown** leaves Mailpit running.

---

## 4. Editors

Each text file has two modes; the admin switches with Form / Full editor.

| Page | File | Form: top half | Form: bottom |
|---|---|---|---|
| Settings | `mailpit.ini` | every key with value, description, Change, Delete | choose a key (grouped by section), value, help and example |
| Tags | `tags.yaml` | rules with test, move up/down, Delete | add match + tags |
| Relay | `relay.yaml` | keys with Change, Delete | choose a key, value, help and example |
| Forward | `forward.yaml` | keys with Change, Delete | choose a key, value, help and example |
| Accounts | `*.auth` | accounts with reset, Delete | add account; turn a file on with its first account, or off |

**Full editor**: CodeMirror 5 (embedded, `properties` or `yaml` mode, dark theme when the system is dark), Verify (shows errors and warnings, marks lines, click to jump), Save, insertable examples per file. A failed save shows the text again with the problems, so nothing typed is lost.

**Turning a file on**: saving a disabled tags, relay or forward file creates it at the standard path and sets its `MP_*` key. Turn off removes the key and keeps the file.

**Secrets**: password hashes show as type and fingerprint; secret settings and relay/forward `password`/`secret` show as `••••` in forms and `__CREW_SECRET_UNCHANGED__` in the editor. Saving the placeholder keeps the value on disk.

**Concurrent edits**: every form carries the hash of the file it was built from; a save from an outdated page is refused.

### 4.1 Verification

| File | Checks |
|---|---|
| `mailpit.ini` | `KEY=value` lines; types (bool, int ranges, `12h`/`30d`, host:port, regex, URL, enums); absolute paths; files exist; TLS cert/key pairs load; TLS/STARTTLS rules; locked keys; port clashes between UI, SMTP, POP3 and Crew; unknown keys (warning) |
| `*.auth` | `user:hash` lines; at least one account; Crew account present in `ui.auth` |
| `tags.yaml` | YAML; `filters` list present (Mailpit fails without it); each filter has text `match` and `tags`; unknown keys (warning) |
| `relay.yaml` | YAML mapping; `host` required; port 1-65535; `auth` none/plain/login/cram-md5 with the needed username/password/secret; tls and starttls not both; regexes compile; `override-from` is an address; unknown keys and deprecated `recipient-allowlist` (warning) |
| `forward.yaml` | as relay, plus `to` required and a list of valid addresses |

---

## 5. Save and restart

1. **Save** (any editor): verify, refuse on errors, back up the old file to `data/backups` (keep 50), write atomically. Mailpit is not restarted.
2. **Not active notice**: Crew keeps a copy of the files Mailpit last started with (`data/active`). Every page shows "N file(s) changed but not active" when a file on disk differs (saved in Crew or edited by hand), with a diff page and a Restart button.
3. **Restart**: verify all files (refuse on errors), `ctl.sh restart`, wait for `readyz`, wait 5 s, check uptime (Mailpit can pass `readyz` and exit a moment later). Success: the files become the active copy.
4. **Failed restart**: nothing is rolled back automatically. A banner shows the error and Mailpit's last log lines, with **Restart again** (after fixing) and **Roll back**. The watchdog pauses.
5. **Roll back**: write the active copy back (current files go to backups), restart, health check.

---

## 6. Configuration

| YAML | Flag | Env | Default |
|---|---|---|---|
| `home` | `--home` | `MPC_HOME` | current directory |
| `listen` | `--listen` | `MPC_LISTEN` | `[::]:8080` |
| `webroot` | `--webroot` | `MPC_WEBROOT` | `/` |
| `tls.cert`, `tls.key` | `--tls-cert`, `--tls-key` | `MPC_TLS_CERT`, `MPC_TLS_KEY` | none |
| `data_dir` | `--data-dir` | `MPC_DATA_DIR` | `<home>/data` |
| `users_file` | `--users-file` | `MPC_USERS_FILE` | `<data_dir>/users.yaml` |
| `log_file`, `log_level` | `--log-file`, `--log-level` | `MPC_LOG_FILE`, `MPC_LOG_LEVEL` | stdout, `info` |
| `managed.version` | `--mailpit-version` | `MPC_MAILPIT_VERSION` | ask |
| `restart.timeout` | `--restart-timeout` | `MPC_RESTART_TIMEOUT` | `30s` |
| `backups.keep` | `--backups-keep` | `MPC_BACKUPS_KEEP` | `50` |
| `session.ttl` | `--session-ttl` | `MPC_SESSION_TTL` | `12h` |

Priority: flags, env vars, config file, defaults. Unknown config keys are errors. `mode: external` from older versions is refused with a clear message.

---

## 7. Security

- Crew login: bcrypt, server-side sessions (`HttpOnly`, `SameSite=Strict`, `Secure` with TLS), login rate limit, CSRF token and Origin check on every POST.
- CSP `default-src 'self'; script-src 'self'` (CodeMirror is embedded, no CDN), `X-Frame-Options: DENY`.
- Crew writes only inside `<home>/mailpit`; password and config files are mode 0600.
- Audit log (JSON lines) of logins, saves, restarts, rollbacks and version changes; no secrets.

---

## 8. Commands

`mailpit-crew` (run), `health` (exit 0 when the web UI answers), `import <mailpit.ini>` (copy an existing setup: database, password files with UI accounts merged into `ui.auth`, YAML files, certificates, settings), `reset-password`, `hash-password`, `version`.

---

## 9. Verified Mailpit behaviour

Checked against Mailpit v1.28.0 (source and a running binary):

1. **Username tag case**: the tag is the SMTP username as typed (title case is not applied). If a tag with the same name in another case already exists (for example `Gitea` from `tags.yaml`), Mailpit reuses that tag.
2. **Bcc**: envelope recipients that are not in To/Cc are added to the stored `Bcc` header, so they appear in message summaries.
3. **API**: `/readyz`, `/api/v1/info` and `/api/v1/search` (used by Crew) exist in v1.28 and v1.32.
4. **Reload**: auth files and `tags.yaml` are read only at startup. Every change needs a restart.
5. **Search OR**: not needed; Crew evaluates rules itself.
6. **Env keys**: the UI listen key is `MP_UI_BIND_ADDR` and SMTP is `MP_SMTP_BIND_ADDR` (not `MP_LISTEN`). Bool values are true for `1`, `true` or `yes`. The catalog is in `internal/catalog`.
7. **Empty tags file**: Mailpit accepts `filters: []`, but fails on a file without a `filters` key (`missing tag: array`).
8. **Ready then crash**: Mailpit answers `readyz` before the SMTP listener binds. A taken SMTP port makes it exit about 300 ms later. The apply flow handles this (section 5).
9. **SMTP username**: message summaries have a `Username` field. Mailpit v1.28.0-v1.28.2 set it only for `AUTH LOGIN`; v1.28.3 fixed `AUTH PLAIN` ("Auto-tagging using SMTP username using plain auth", #617). Crew warns for older versions.
10. **Dangerous API defaults**: `DELETE /api/v1/messages` with no IDs (or an unreadable body) deletes **all** messages, and `PUT /api/v1/messages` with no IDs marks all messages read. The ID `latest` means the newest message.
11. **Read state**: `GET /api/v1/message/{id}` does not mark a message read.
12. **Upgrade**: switching a live install from v1.28.0 to v1.32.0 through the Mailpit page kept the database and mail.


---

## 10. Docker

- `Dockerfile`: Go build stage, Alpine runtime with `ca-certificates`, `tini` and `tzdata`. `ENTRYPOINT tini -g -- mailpit-crew`, so tini reaps Mailpit (started detached by `ctl.sh`) and sends stop signals to the whole process group. `MPC_HOME=/crew` (volume), `MPC_LISTEN=[::]:8080`, ports 8080, 8025, 1025, 1110. Health check `mailpit-crew health`.
- `docker-compose.yml`: one service, named volume for `/crew`, `MPC_MAILPIT_VERSION=latest` for the first start, `restart: unless-stopped`, `stop_grace_period: 30s`.
- Mailpit ports in the settings and published ports must match; Crew does not change the compose file.
