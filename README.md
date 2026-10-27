# MailPit-Crew

Install, configure and run [Mailpit](https://github.com/axllent/mailpit) from one program. MailPit-Crew downloads the Mailpit version you choose, runs it through a small control script, and lets you edit every Mailpit setting, password file and YAML file in a web UI, with validation and examples. Mail itself is read in Mailpit's own web UI, as before.

> MailPit-Crew is an unofficial companion tool. It is not part of, or endorsed by, the Mailpit project. Mailpit itself is not modified.

## Features

- **One program**: run `mailpit-crew`; it installs and runs Mailpit. No separate Mailpit service.
- **Mailpit versions**: pick latest, latest-1, latest-2, any other tag, or your own binary. Switch from the web UI; a version that does not start is rolled back to the previous one.
- **Control script**: Crew runs Mailpit through `mailpit/ctl.sh` (`start | stop | restart | status | version`). You can use it by hand too. A watchdog starts Mailpit again if it stops.
- **Every file managed in Crew**:
  - Mailpit settings (`mailpit.ini`, all `MP_*` keys)
  - password files: web UI and API (`ui.auth`), SMTP, POP3, Send API
  - tag rules (`tags.yaml`), SMTP relay (`relay.yaml`), SMTP forward (`forward.yaml`)
- **Two ways to edit**, switch any time:
  - **Form**: the top half shows what is in the file, with **Delete** (and **Change**) on every item; the form below adds new items. Each key shows a description and an example.
  - **Full editor**: syntax-highlighted editor (CodeMirror) with **Verify**, problems marked on their lines, and ready-made **examples** to insert.
- **Verified the way Mailpit reads them**: types, ports, regular expressions, email addresses, TLS cert/key pairs, required keys (relay `host`, forward `to`), auth needs (`plain` needs username and password), port clashes, unknown keys as warnings.
- **Save, then restart when you choose**: saving validates, backs up and writes the file, but does not restart Mailpit. A banner shows "N file(s) changed but not active" (also for hand edits), with the diff and a **Restart Mailpit** button.
- **Failed restart**: Crew shows the error and Mailpit's last log lines, and asks: fix the files and restart again, or **Roll back** to the files Mailpit last ran with.
- **Secrets stay hidden**: password hashes, relay and forward passwords and secret settings are masked in forms, diffs and the editor.
- **Import** an existing Mailpit setup. **Audit log** of every login and change.

## Folder layout

```
/opt/mailpit-crew/
  mailpit-crew            Crew binary
  config.yaml             Crew settings (optional)
  data/
    users.yaml            Crew admin login
    state.yaml            Crew's Mailpit API password (generated)
    active/               copy of the files Mailpit last started with
    backups/              one file per save
    audit.log
  mailpit/
    ctl.sh                start | stop | restart | status | version
    bin/
      mailpit-v1.32.0     downloaded releases
      current -> mailpit-v1.32.0
    mailpit.ini           Mailpit settings
    ui.auth               web UI logins (+ Crew's hidden API account)
    smtp.auth, pop3.auth, send-api.auth
    tags.yaml, relay.yaml, forward.yaml
    mailpit.db            mail storage
    mailpit.log           Mailpit output
```

## Quick start

```sh
mkdir -p /opt/mailpit-crew && cd /opt/mailpit-crew
# copy the mailpit-crew binary here, then:
./mailpit-crew --home /opt/mailpit-crew
```

On first start Crew:

1. creates the folders and Mailpit's files,
2. creates the Crew login `admin` and prints its password **once**,
3. asks which Mailpit to run (when started in a terminal; versions below are an example):

   ```
   No Mailpit version is selected yet. Which Mailpit should Crew run?
     1) v1.32.0  (latest, 2026-10-01)
     2) v1.31.6  (latest-1, 2026-09-01)
     3) v1.31.5  (latest-2, 2026-08-20)
     4) other: a version tag (v1.28.0) or the path to a Mailpit binary
     Enter) skip, choose later in the web UI
   ```

Without a terminal (systemd, Docker), set `managed.version` (`latest`, a tag, or a path), or choose on the **Mailpit** page.

Ports:

| What | Default |
|---|---|
| Crew admin UI | `8080` (`listen`) |
| Mailpit web UI and API, where mail is read | `8025` (`MP_UI_BIND_ADDR`) |
| Mailpit SMTP | `1025` (`MP_SMTP_BIND_ADDR`) |
| Mailpit POP3 (when turned on) | `1110` |

Open `http://your-host:8080`, log in as `admin`, and change the password (click `admin` at the top right). Forgot it? Run `mailpit-crew reset-password --home /opt/mailpit-crew`.

## Typical setup

1. **Accounts** → Web UI & API accounts: add logins for the people who read mail in Mailpit.
2. **Accounts** → SMTP: **Turn on** with the first app account (for example `gitea`), add more.
3. **Settings**: TLS for the web UI and SMTP (`MP_UI_TLS_CERT`, `MP_SMTP_TLS_CERT`, ...), limits (`MP_MAX_MESSAGES`, `MP_MAX_AGE`). The full editor has examples for these.
4. **Tags**, **Relay**, **Forward** as needed.
5. Click **Restart Mailpit** in the banner.

## Run with systemd

Copy [deploy/systemd/mailpit-crew.service](deploy/systemd/mailpit-crew.service) to `/etc/systemd/system/`, put a [config.example.yaml](config.example.yaml) based `config.yaml` in `/opt/mailpit-crew`, then:

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

| From the old setup | To |
|---|---|
| `MP_DATABASE` (with `-wal`/`-shm`) | `mailpit/mailpit.db` |
| `MP_UI_AUTH_FILE` accounts | `mailpit/ui.auth` (same passwords) |
| `MP_SMTP_AUTH_FILE`, `MP_POP3_AUTH_FILE`, `MP_SEND_API_AUTH_FILE` | `smtp.auth`, `pop3.auth`, `send-api.auth` |
| `MP_TAGS_CONFIG`, `MP_SMTP_RELAY_CONFIG`, `MP_SMTP_FORWARD_CONFIG` | `tags.yaml`, `relay.yaml`, `forward.yaml` |
| TLS certificates and keys | `mailpit/ssl/` |
| other `MP_*` settings | `mailpit/mailpit.ini` |

`MP_UI_AUTH`, `MP_WEBROOT` and `MP_LOG_FILE` are managed by Crew and not copied. Then start Crew and choose a Mailpit version.

## Settings managed by Crew

| Setting | Value | Why |
|---|---|---|
| `MP_DATABASE` | `<home>/mailpit/mailpit.db` | mail stays in Crew's folder |
| `MP_UI_AUTH_FILE` | `<home>/mailpit/ui.auth` | holds Crew's API account next to your logins |
| `MP_UI_AUTH` | not set | would replace `ui.auth` |
| `MP_WEBROOT` | not set | Crew calls the API at `/` |
| `MP_LOG_FILE` | not set | `ctl.sh` writes Mailpit's output to `mailpit.log` |

These are shown read-only and reset on start if edited by hand. Everything else is yours.

## Commands

```sh
mailpit-crew [flags]                         run Crew, which runs Mailpit
mailpit-crew import [flags] <mailpit.ini>    import an existing Mailpit setup
mailpit-crew reset-password [flags]          new random password for the Crew admin
mailpit-crew hash-password                   bcrypt hash for an htpasswd line
mailpit-crew version
mailpit-crew --help                          all flags (each has an MPC_* env var)
```

## Configuration reference

| YAML | Flag | Env | Default |
|---|---|---|---|
| `home` | `--home` | `MPC_HOME` | current directory |
| `listen` | `--listen` | `MPC_LISTEN` | `[::]:8080` |
| `webroot` | `--webroot` | `MPC_WEBROOT` | `/` |
| `tls.cert` / `tls.key` | `--tls-cert` / `--tls-key` | `MPC_TLS_CERT` / `MPC_TLS_KEY` | none (HTTP) |
| `data_dir` | `--data-dir` | `MPC_DATA_DIR` | `<home>/data` |
| `users_file` | `--users-file` | `MPC_USERS_FILE` | `<data_dir>/users.yaml` |
| `log_file` | `--log-file` | `MPC_LOG_FILE` | stdout |
| `log_level` | `--log-level` | `MPC_LOG_LEVEL` | `info` |
| `managed.version` | `--mailpit-version` | `MPC_MAILPIT_VERSION` | none (ask) |
| `restart.timeout` | `--restart-timeout` | `MPC_RESTART_TIMEOUT` | `30s` |
| `backups.keep` | `--backups-keep` | `MPC_BACKUPS_KEEP` | `50` |
| `session.ttl` | `--session-ttl` | `MPC_SESSION_TTL` | `12h` |

## Notes

- **Downloads** come from `https://github.com/axllent/mailpit/releases` (`mailpit-<os>-<arch>.tar.gz`). Mailpit releases publish no checksums; Crew checks that the downloaded binary reports the expected version. Offline: use option 4 with a binary path.
- **Crew reaches Mailpit's API** on the web UI address from `MP_UI_BIND_ADDR` (`127.0.0.1` for wildcard addresses), over HTTPS when `MP_UI_TLS_CERT` is set, without checking the certificate name (it is issued for your host name, not `127.0.0.1`).
- **ctl.sh** reads `mailpit.ini` without `eval`, so values are never run as shell code.
- **Paths must be absolute.** Mailpit resolves relative paths from its working directory, so Crew rejects them.
- **Concurrent edits are detected**: if a file changed since you opened the page, saving stops and asks you to reload.
- Tested with Mailpit v1.28 and v1.32.

## Build

```sh
make build    # or: go build -o mailpit-crew ./cmd/mailpit-crew
make test
```

## License

MIT. CodeMirror 5 (embedded editor) is MIT licensed, see `internal/web/assets/static/vendor/codemirror/LICENSE`.
