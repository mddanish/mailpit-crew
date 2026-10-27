# MailPit-Crew

Run and manage [Mailpit](https://github.com/axllent/mailpit) from one program. MailPit-Crew downloads the Mailpit version you choose, writes its settings, password files and tag rules from a web UI with validation, and starts, stops and restarts it through a small control script. Every change is applied safely: Crew backs up the files, writes them, restarts Mailpit, checks it stays up, and rolls back automatically if it does not.

> MailPit-Crew is an unofficial companion tool. It is not part of, or endorsed by, the Mailpit project. Mailpit itself is not modified.

Status: **phase 1** (admin panel and managed Mailpit). Per-user inboxes come in phase 2, a per-user POP3 server and Docker images in phase 3. See [SPEC.md](SPEC.md).

## Features

- **One program**: run `mailpit-crew`; it installs and runs Mailpit. No separate Mailpit service.
- **Mailpit versions**: pick latest, latest-1, latest-2, any other tag, or your own binary. Switch from the web UI; a version that does not start is rolled back to the previous one.
- **Control script**: Crew runs Mailpit through `mailpit/ctl.sh` (`start | stop | restart | status | version`). You can use it by hand too. A watchdog starts Mailpit again if it stops.
- **Settings**: form for every known `MP_*` setting with type checks. Settings that Crew depends on are shown as "managed by Crew" and cannot be broken by accident. Raw text editor for anything else. Secrets are never shown.
- **Accounts**: SMTP, POP3 and Send API password files, turned on with one click. bcrypt hashes, generated passwords, warnings for shared hashes.
- **Tag rules**: edit `tags.yaml` as a table, reorder, test a rule against stored mail.
- **Safe apply**: changes are staged, shown as a diff, then applied with validation, backup, atomic write, restart, health check and rollback.
- **Import**: bring an existing Mailpit setup (settings, database, password files, tag rules, certificates) into Crew. Web UI accounts become Crew logins with the same password.
- **Audit log** of every login and change.

## Folder layout

Crew creates everything under its home folder (`--home`, default: current directory):

```
/opt/mailpit-crew/
  mailpit-crew            Crew binary
  config.yaml             Crew settings (optional)
  data/
    users.yaml            Crew logins
    state.yaml            Crew's Mailpit API password (generated)
    backups/              one folder per apply
    audit.log
  mailpit/
    ctl.sh                start | stop | restart | status | version
    bin/
      mailpit-v1.32.0     downloaded releases
      current -> mailpit-v1.32.0
    mailpit.ini           Mailpit settings
    ui.auth               Crew's API account (managed by Crew)
    smtp.auth, pop3.auth  optional password files
    tags.yaml
    mailpit.db            mail storage
    mailpit.log           Mailpit output
    mailpit.pid
```

## Quick start

```sh
mkdir -p /opt/mailpit-crew && cd /opt/mailpit-crew
# copy the mailpit-crew binary here, then:
./mailpit-crew --home /opt/mailpit-crew
```

On first start Crew:

1. creates the folders and Mailpit's files,
2. creates a Crew login `admin` and prints its password **once**,
3. asks which Mailpit to run (when started in a terminal; versions below are an example):

   ```
   No Mailpit version is selected yet. Which Mailpit should Crew run?
     1) v1.32.0  (latest, 2026-10-01)
     2) v1.31.2  (latest-1, 2026-09-01)
     3) v1.31.1  (latest-2, 2026-08-20)
     4) other: a version tag (v1.28.0) or the path to a Mailpit binary
     Enter) skip, choose later in the web UI
   ```

Without a terminal (systemd, Docker), set `managed.version` (`latest`, a tag, or a path), or choose on the **Mailpit** page of the web UI.

Open `http://your-host:8025`, log in, and change the password (click your user name).

Mailpit's SMTP server listens on `[::]:1025` by default. The Mailpit API listens on `127.0.0.1:8026` and is only used by Crew.

## Run with systemd

1. Put the binary and a config in `/opt/mailpit-crew` (see [config.example.yaml](config.example.yaml)):

   ```yaml
   home: /opt/mailpit-crew
   listen: "[::]:8025"
   tls:
     cert: /opt/mailpit-crew/mailpit/ssl/mail.example.com.fullchain.pem
     key: /opt/mailpit-crew/mailpit/ssl/mail.example.com.key.pem
   managed:
     version: latest
   ```

2. Copy [deploy/systemd/mailpit-crew.service](deploy/systemd/mailpit-crew.service) to `/etc/systemd/system/`, then:

   ```sh
   systemctl daemon-reload
   systemctl enable --now mailpit-crew
   journalctl -u mailpit-crew | grep password   # first admin password
   ```

The unit uses `KillMode=process`, so Mailpit keeps running while Crew restarts or upgrades. Stop Mailpit with `/opt/mailpit-crew/mailpit/ctl.sh stop`.

## Move an existing Mailpit setup into Crew

Example: Mailpit in `/opt/mailpit` with `/opt/mailpit/mailpit.ini` and its own `mailpit.service`.

```sh
systemctl disable --now mailpit            # stop the old Mailpit first
cd /opt/mailpit-crew
./mailpit-crew import --home /opt/mailpit-crew /opt/mailpit/mailpit.ini
```

Import copies:

| From the old setup | To |
|---|---|
| `MP_DATABASE` (with `-wal`/`-shm`) | `mailpit/mailpit.db` |
| `MP_SMTP_AUTH_FILE`, `MP_POP3_AUTH_FILE`, `MP_SEND_API_AUTH_FILE` | `mailpit/smtp.auth`, `pop3.auth`, `send-api.auth` |
| `MP_TAGS_CONFIG` | `mailpit/tags.yaml` |
| TLS certificates and keys | `mailpit/ssl/` |
| `MP_UI_AUTH_FILE` accounts | Crew admin logins, same password |
| other `MP_*` settings | `mailpit/mailpit.ini` |

`MP_UI_BIND_ADDR`, `MP_UI_TLS_*`, `MP_WEBROOT`, `MP_LOG_FILE` and `MP_TAGS_USERNAME` are managed by Crew and not copied. The old web UI TLS certificate is copied to `mailpit/ssl/`; point `tls.cert` and `tls.key` in the Crew config at it, so Crew serves HTTPS on port 8025 as Mailpit did.

Then start Crew and choose a Mailpit version.

## Settings managed by Crew

In managed mode these Mailpit settings are fixed, shown read-only, and reset on start if edited by hand:

| Setting | Value |
|---|---|
| `MP_DATABASE` | `<home>/mailpit/mailpit.db` |
| `MP_UI_BIND_ADDR` | `managed.api_listen` (local only) |
| `MP_UI_AUTH_FILE` | `<home>/mailpit/ui.auth` (Crew's API account) |
| `MP_TAGS_USERNAME` | `true` |
| `MP_UI_AUTH`, `MP_UI_TLS_CERT`, `MP_UI_TLS_KEY`, `MP_WEBROOT`, `MP_LOG_FILE` | not set |

Everything else is yours to set in the web UI.

## External mode

To keep running Mailpit yourself (its own systemd unit, container or script), set `mode: external` and point Crew at Mailpit's env file. Crew then edits the files and restarts Mailpit with your command (`restart.mode: command`, for example `systemctl restart mailpit`) or as a child process (`restart.mode: child`). See the external section in [config.example.yaml](config.example.yaml).

## Commands

```sh
mailpit-crew [flags]                         run Crew (and Mailpit, in managed mode)
mailpit-crew import [flags] <mailpit.ini>    import an existing Mailpit setup
mailpit-crew reset-password [flags] <user>   new random password for a Crew login
mailpit-crew hash-password                   bcrypt hash for an htpasswd line
mailpit-crew version
mailpit-crew --help                          all flags (each has an MPC_* env var)
```

## Configuration reference

| YAML | Flag | Env | Default |
|---|---|---|---|
| `mode` | `--mode` | `MPC_MODE` | `managed` |
| `home` | `--home` | `MPC_HOME` | current directory |
| `listen` | `--listen` | `MPC_LISTEN` | `[::]:8025` |
| `webroot` | `--webroot` | `MPC_WEBROOT` | `/` |
| `tls.cert` / `tls.key` | `--tls-cert` / `--tls-key` | `MPC_TLS_CERT` / `MPC_TLS_KEY` | none (HTTP) |
| `data_dir` | `--data-dir` | `MPC_DATA_DIR` | `<home>/data` |
| `users_file` | `--users-file` | `MPC_USERS_FILE` | `<data_dir>/users.yaml` |
| `log_file` | `--log-file` | `MPC_LOG_FILE` | stdout |
| `log_level` | `--log-level` | `MPC_LOG_LEVEL` | `info` |
| `managed.version` | `--mailpit-version` | `MPC_MAILPIT_VERSION` | none (ask) |
| `managed.api_listen` | `--mailpit-api-listen` | `MPC_MAILPIT_API_LISTEN` | `127.0.0.1:8026` |
| `restart.timeout` | `--restart-timeout` | `MPC_RESTART_TIMEOUT` | `30s` |
| `backups.keep` | `--backups-keep` | `MPC_BACKUPS_KEEP` | `20` |
| `session.ttl` | `--session-ttl` | `MPC_SESSION_TTL` | `12h` |

External mode only: `mailpit.api_url`, `mailpit.api_user`, `mailpit.api_password`, `mailpit.env_file`, `mailpit.managed_dirs`, `mailpit.*_file`, `restart.mode`, `restart.command`, `child.binary`, `child.args` (flags `--mailpit-*`, `--restart-*`).

## Notes

- **Downloads** come from `https://github.com/axllent/mailpit/releases` (`mailpit-<os>-<arch>.tar.gz`). Mailpit releases publish no checksums; Crew checks the downloaded binary reports the expected version before using it. Offline: use option 4 with a binary path.
- **ctl.sh** reads `mailpit.ini` without `eval`, so setting values are never run as shell code. Crew only replaces `ctl.sh` when a newer template ships, and keeps the previous one as `ctl.sh.old`.
- **Paths must be absolute.** Mailpit resolves relative paths from its working directory, so Crew rejects them.
- **Hand edits are detected.** If a file changes on disk after you staged a change, apply stops and asks you to discard and edit again.
- Tested with Mailpit v1.28.

## Build

```sh
make build    # or: go build -o mailpit-crew ./cmd/mailpit-crew
make test
```

## License

MIT
