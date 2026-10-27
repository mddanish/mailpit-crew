# MailPit-Crew: one container that installs, configures and runs Mailpit.
#
#   docker build -t mailpit-crew .
#   docker run -d -p 8080:8080 -p 8025:8025 -p 1025:1025 -v mailpit-crew:/crew mailpit-crew

# --- build ---
# Runs on the build machine's platform and cross-compiles for the target,
# so multi-arch builds (amd64, arm64) need no emulation for Go.
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/mailpit-crew ./cmd/mailpit-crew

# --- run ---
FROM alpine:3.20
# ca-certificates: download Mailpit releases from GitHub
# tini: PID 1, reaps Mailpit (started detached by ctl.sh) and forwards
#       stop signals to the whole process group, so Mailpit stops cleanly
# tzdata: TZ for Mailpit and Crew logs
RUN apk add --no-cache ca-certificates tini tzdata

COPY --from=build /out/mailpit-crew /usr/local/bin/mailpit-crew

# Everything Crew and Mailpit keep (login, settings, mail database,
# downloaded Mailpit binaries) lives in /crew.
ENV MPC_HOME=/crew \
    MPC_LISTEN=[::]:8080
VOLUME /crew
WORKDIR /crew

# 8080 Crew admin UI, 8025 Mailpit web UI, 1025 SMTP, 1110 POP3 (when on)
EXPOSE 8080 8025 1025 1110

HEALTHCHECK --interval=30s --timeout=10s --start-period=60s --retries=3 \
    CMD ["mailpit-crew", "health"]

ENTRYPOINT ["/sbin/tini", "-g", "--", "mailpit-crew"]
