# Changelog

All notable changes to MailPit-Crew are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- README badges: release, release workflow, CI, Docker image, downloads and license.

## [0.4.3] - 2026-10-27

### Changed

- `docker-compose.yml` runs the prebuilt `ghcr.io/mddanish/mailpit-crew:latest` image instead of building locally. To pin a release, use a tag such as `:0.4.3`. To build from source, replace `image:` with `build: .`.

## [0.4.2] - 2026-10-27

### Fixed

- Settings with SMTP authentication but no SMTP TLS certificate, and without `MP_SMTP_AUTH_ALLOW_INSECURE=true`, are now reported as an error on save. Before, Mailpit failed on the next restart. This applies to an auth file, inline credentials or accept-any.
- Turning on SMTP accounts without a TLS certificate also sets `MP_SMTP_AUTH_ALLOW_INSECURE=true` in the same save, and the confirmation message says so.

## [0.4.1] - 2026-10-27

### Added

- Docker image published to GitHub Container Registry on every release: `ghcr.io/mddanish/mailpit-crew`, for `linux/amd64` and `linux/arm64`, with tags `latest`, `X.Y.Z` and `X.Y`.
- Screenshots of the web UI in the README.

### Changed

- The Dockerfile build stage cross-compiles for the target platform, so multi-arch builds need no emulation for Go.

## [0.4.0] - 2026-10-27

First public release.

### Added

- Crew installs, configures and runs a stock Mailpit. Mailpit itself is not modified.
- Mailpit version on first start: latest, latest-1, latest-2, another tag, or your own binary. You can switch versions in the web UI. A version that does not start is rolled back to the previous one.
- Crew runs Mailpit through `mailpit/ctl.sh` (`start | stop | restart | status | version`). No separate Mailpit service is needed. A watchdog starts Mailpit again if it stops.
- The web UI manages:
  - `mailpit.ini`
  - password files for the web UI and API, SMTP, POP3 and the Send API
  - `tags.yaml`, `relay.yaml` and `forward.yaml`
- Two ways to edit each file. In the form, the top half lists what is in the file, with Change and Delete on each item, and the form below adds items. The full editor uses CodeMirror with syntax highlighting, Verify and examples.
- Files are checked the way Mailpit reads them, with problems shown on their lines.
- Saving does not restart Mailpit. A banner lists the files Mailpit is not using yet, with a diff and a Restart button.
- When a restart fails, Crew shows the error and Mailpit's last log lines. You can fix the files and restart again, or roll back to the files Mailpit last ran with.
- Backups of every saved file, and detection of changes made by someone else at the same time.
- Secrets are masked in forms, diffs and the editor.
- One admin login, with `mailpit-crew reset-password` on the command line.
- `mailpit-crew import` to move an existing Mailpit setup into Crew.
- An audit log of every login and change.
- Configuration through a config file, `MPC_*` environment variables or command-line flags.
- systemd unit, `Dockerfile` and `docker-compose.yml` that run Crew and Mailpit in one container, and a `mailpit-crew health` command for the container health check.
- GitHub Actions:
  - CI runs the tests and a Docker build.
  - On every `v*` tag, a release is published with Linux amd64 and arm64 binaries and `SHA256SUMS`.

[Unreleased]: https://github.com/mddanish/mailpit-crew/compare/v0.4.3...HEAD
[0.4.3]: https://github.com/mddanish/mailpit-crew/compare/v0.4.2...v0.4.3
[0.4.2]: https://github.com/mddanish/mailpit-crew/compare/v0.4.1...v0.4.2
[0.4.1]: https://github.com/mddanish/mailpit-crew/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/mddanish/mailpit-crew/releases/tag/v0.4.0
