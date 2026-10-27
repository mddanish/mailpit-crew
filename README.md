# MailPit-Crew

An admin panel for [Mailpit](https://github.com/axllent/mailpit). Edit Mailpit settings, password files and tag rules in a web UI with validation, then apply them safely: Crew backs up the files, writes them, restarts Mailpit, checks it stays up, and rolls back automatically if it does not.

> MailPit-Crew is an unofficial companion tool. It is not part of, or endorsed by, the Mailpit project. Mailpit itself is not modified.

Status: **phase 1** (admin panel). Per-user inboxes come in phase 2, a per-user POP3 server and Docker images in phase 3. See [SPEC.md](SPEC.md).

## Features (phase 1)

- **Settings**: form for every known `MP_*` setting, grouped by section, with type checks (numbers, durations, addresses, regex, files, TLS cert/key pairs). Raw text editor for anything else. Secrets are never shown.
- **Accounts**: manage Mailpit password files (`MP_SMTP_AUTH_FILE`, `MP_UI_AUTH_FILE`, `MP_POP3_AUTH_FILE`, `MP_SEND_API_AUTH_FILE`). bcrypt hashes, generated passwords, warnings for shared hashes and weak hash types.
- **Tag rules**: edit `MP_TAGS_CONFIG` as a table, reorder, test a rule against stored mail. Comments in the file are kept.
- **Safe apply**: changes are staged first. Review a diff, then apply. Validation, backup, atomic write, restart, health check (Mailpit must stay up), automatic rollback.
- **Status**: Mailpit version, uptime, message count, and checks for risky settings.
- **Audit log** of every login and change (`<data_dir>/audit.log`).
- One static binary, no database, no frontend build step.

## How it works

```
Browser ──HTTPS──▶ MailPit-Crew :8025 ──HTTP──▶ Mailpit API 127.0.0.1:8026
                         │
                         └── writes mailpit.ini, *.auth, tags.yaml, then restarts Mailpit
Apps ──SMTP──▶ Mailpit :1025 (unchanged)
```

Restart modes:

| Mode | Use when | How Crew restarts Mailpit |
|---|---|---|
| `command` | Mailpit runs under systemd, Docker or a script | Runs your command, for example `systemctl restart mailpit` |
| `child` | Bare metal, or one container with both | Crew starts Mailpit itself and restarts it |

## Install

Build (Go 1.24+):

```sh
make build        # or: go build -o mailpit-crew ./cmd/mailpit-crew
```

Commands:

```sh
mailpit-crew --config config.yaml        # run
mailpit-crew --help                      # all flags (each has an MPC_* env var)
mailpit-crew hash-password               # bcrypt hash for an htpasswd line
mailpit-crew reset-password --config config.yaml admin
mailpit-crew version
```

On first start Crew creates a Crew admin user `admin` and prints its password **once** in the log. Log in and change it under your user name (top right).

## Setup with systemd (Mailpit already installed)

Example for Mailpit in `/opt/mailpit` with `EnvironmentFile=/opt/mailpit/mailpit.ini`.

1. **Create a service account for Crew** in Mailpit's UI password file:

   ```sh
   echo -n 'a-long-random-password' | mailpit-crew hash-password
   # add a line to /opt/mailpit/users.auth:
   # crew:$2a$10$...
   ```

2. **Move the Mailpit UI to localhost.** In `mailpit.ini`:

   ```ini
   MP_UI_BIND_ADDR=127.0.0.1:8026
   ```

   Remove `MP_UI_TLS_CERT` and `MP_UI_TLS_KEY` (Crew serves HTTPS instead). Then `systemctl restart mailpit`.

3. **Configure Crew** in `/opt/mailpit-crew/config.yaml` (see [config.example.yaml](config.example.yaml)):

   ```yaml
   listen: "[::]:8025"
   tls:
     cert: /opt/mailpit/ssl/mail.example.com.fullchain.pem
     key: /opt/mailpit/ssl/mail.example.com.key.pem
   data_dir: /opt/mailpit-crew/data
   mailpit:
     api_url: http://127.0.0.1:8026
     api_user: crew
     api_password: "a-long-random-password"
     env_file: /opt/mailpit/mailpit.ini
   restart:
     mode: command
     command: ["systemctl", "restart", "mailpit"]
   ```

   Keep this file private (`chmod 600`), it holds the service account password.

4. **Install the service**: copy [deploy/systemd/mailpit-crew.service](deploy/systemd/mailpit-crew.service) to `/etc/systemd/system/`, then:

   ```sh
   systemctl daemon-reload
   systemctl enable --now mailpit-crew
   journalctl -u mailpit-crew | grep password   # first admin password
   ```

5. Open `https://your-host:8025`. The **Status** page lists anything still risky, for example POP3 sharing the UI password file.

## Setup on bare metal (child mode)

Crew starts Mailpit with the env file and restarts it on changes:

```sh
mailpit-crew \
  --restart-mode child \
  --mailpit-binary /opt/mailpit/mailpit \
  --mailpit-env-file /opt/mailpit/mailpit.ini \
  --mailpit-api http://127.0.0.1:8026 \
  --mailpit-api-user crew --mailpit-api-password 'a-long-random-password' \
  --data-dir /opt/mailpit-crew/data
```

Do not also run Mailpit as a separate service in this mode.

## Docker

Planned for phase 3 (single container in `child` mode, or two containers with `command` mode).

## Configuration reference

| YAML | Flag | Env | Default |
|---|---|---|---|
| `listen` | `--listen` | `MPC_LISTEN` | `[::]:8025` |
| `webroot` | `--webroot` | `MPC_WEBROOT` | `/` |
| `tls.cert` / `tls.key` | `--tls-cert` / `--tls-key` | `MPC_TLS_CERT` / `MPC_TLS_KEY` | none (HTTP) |
| `data_dir` | `--data-dir` | `MPC_DATA_DIR` | `./data` |
| `users_file` | `--users-file` | `MPC_USERS_FILE` | `<data_dir>/users.yaml` |
| `log_file` | `--log-file` | `MPC_LOG_FILE` | stdout |
| `log_level` | `--log-level` | `MPC_LOG_LEVEL` | `info` |
| `mailpit.api_url` | `--mailpit-api` | `MPC_MAILPIT_API` | `http://127.0.0.1:8026` |
| `mailpit.api_user` | `--mailpit-api-user` | `MPC_MAILPIT_API_USER` | none |
| `mailpit.api_password` | `--mailpit-api-password` | `MPC_MAILPIT_API_PASSWORD` | none |
| `mailpit.env_file` | `--mailpit-env-file` | `MPC_MAILPIT_ENV_FILE` | none |
| `mailpit.managed_dirs` | `--mailpit-managed-dirs` | `MPC_MAILPIT_MANAGED_DIRS` | env file directory |
| `mailpit.*_file` | `--mailpit-*-file` | `MPC_MAILPIT_*_FILE` | read from env file |
| `restart.mode` | `--restart-mode` | `MPC_RESTART_MODE` | `command` |
| `restart.command` | `--restart-command` | `MPC_RESTART_COMMAND` | none |
| `restart.timeout` | `--restart-timeout` | `MPC_RESTART_TIMEOUT` | `30s` |
| `child.binary` | `--mailpit-binary` | `MPC_MAILPIT_BINARY` | `mailpit` |
| `child.args` | `--mailpit-args` | `MPC_MAILPIT_ARGS` | none |
| `backups.keep` | `--backups-keep` | `MPC_BACKUPS_KEEP` | `20` |
| `session.ttl` | `--session-ttl` | `MPC_SESSION_TTL` | `12h` |

## Notes

- **Paths must be absolute.** Mailpit resolves relative paths from its working directory (`/` under systemd), so Crew rejects them.
- **Crew only writes inside `managed_dirs`.** Files elsewhere are shown read-only.
- **Hand edits are detected.** If a file changes on disk after you staged a change, apply stops and asks you to discard and edit again.
- **Changing `MP_UI_BIND_ADDR`** also needs `mailpit.api_url` updated. Otherwise the health check fails and Crew rolls back.
- **Backups** of every apply are in `<data_dir>/backups/<time>/` with a `manifest.json`.
- Tested with Mailpit v1.28.

## License

MIT
