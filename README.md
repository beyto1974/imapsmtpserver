# ImapSmtpServer

A very simple dev-only SMTP/IMAP mail catcher with a web UI, in the spirit of
[Mailpit](https://github.com/axllent/mailpit). Written in Go. No auth, no
relaying, no persistence — everything lives in memory and is gone on restart.

Multiple mail accounts are supported: any email address that has sent or
received mail is automatically its own account (nothing to configure), and
the web UI can compose new messages and reply to existing ones - "send" and
"reply" both submit to the server's own SMTP port, so composed mail flows
through the exact same path as mail from an external client.

![Web UI screenshot](docs/screenshot.png)

## Status

Done:
- [x] `internal/store` — thread-safe in-memory message store (add/list/get/delete/clear, UID tracking for IMAP, `\Seen` flag), plus per-account `Inbox`/`Sent` views and change notifications for SSE
- [x] `internal/mailparse` — parses raw RFC822 bytes into subject/from/to/date/text/html/attachments/Message-Id/In-Reply-To using `go-message/mail`
- [x] `internal/smtpd` — minimal SMTP server (`github.com/emersion/go-smtp`), accepts any sender/recipient, no auth required, parses each message and adds it to the store
- [x] `internal/imapd` — IMAP server (`github.com/emersion/go-imap`); the login username selects the account, exposing that account's `INBOX` (received) and `Sent` (sent) mailboxes. Mail normally arrives via SMTP, but `APPEND` is also supported directly into either mailbox (mailbox management and `COPY` remain unsupported)
- [x] `internal/web` — REST API + embedded static frontend: list/view/download/clear mail, browse by account/folder, compose and reply (`POST /api/send`), live updates over Server-Sent Events (`GET /api/events`)
- [x] `cmd/imapsmtpserver/main.go` — wires SMTP + IMAP + web servers together, graceful shutdown on SIGINT/SIGTERM, ports configurable via `-smtp-port`/`-imap-port`/`-web-port`
- [x] End-to-end tests (`cmd/imapsmtpserver/e2e_test.go`): single-account SMTP → web → IMAP → clear flow, a multi-account test that sends alice → bob, replies bob → alice, and checks each account's IMAP INBOX/Sent are correctly isolated, and an IMAP `APPEND` test covering both mailboxes
- [x] `.github/workflows/release.yml` — on pushing a `v*.*.*` tag, cross-compiles binaries for linux/windows/darwin (amd64 + arm64, except windows/arm64) and uploads them to a GitHub Release, and builds/pushes a multi-arch Docker image to GHCR
- [x] `Dockerfile` / `docker-compose.yml` — multi-stage build onto a distroless base image, `-host 0.0.0.0` so the mapped ports are reachable from the host

## Running

```sh
go build ./...
go run ./cmd/imapsmtpserver
```

Ports default to 1025 (SMTP), 1143 (IMAP) and 8025 (web), and can be
overridden, along with the bind address (`-host`, default `127.0.0.1`):

```sh
go run ./cmd/imapsmtpserver -smtp-port 2525 -imap-port 1144 -web-port 8080
```

| Flag          | Default     | Description                                                                                  |
| ------------- | ----------- | ---------------------------------------------------------------------------------------------|
| `-host`       | `127.0.0.1` | Address to bind the SMTP/IMAP/web servers to. Use `0.0.0.0` to accept connections from other hosts (e.g. in Docker). |
| `-smtp-port`  | `1025`      | SMTP server port.                                                                             |
| `-imap-port`  | `1143`      | IMAP server port.                                                                             |
| `-web-port`   | `8025`      | Web UI/API port.                                                                              |

Run `go run ./cmd/imapsmtpserver -h` to see this from the binary itself.

Or with Docker Compose:

```sh
docker compose up --build
```

which builds the image and maps ports 1025/1143/8025 to the host. The
container runs with `-host 0.0.0.0` so it's reachable from outside; the web
UI's own compose/reply feature still submits over loopback inside the
container regardless of `-host`.

## Deploying with Docker

Tagged releases (`v*.*.*`) are published as a multi-arch (`linux/amd64`,
`linux/arm64`) image to GitHub Container Registry:

```sh
docker run -d \
  -p 1025:1025 -p 1143:1143 -p 8025:8025 \
  ghcr.io/beyto1974/imapsmtpserver:latest
```

Available tags: `latest`, a full version (`1.2.3`), and a major.minor
(`1.2`) — all pointing at the same image for a given release. There's no
`:v0.1.0`-style tag; the leading `v` from the git tag is stripped. See
[Releases](#releases) for how these get built.

Or point `docker-compose.yml` at the published image instead of building
locally, by replacing `build: .` with `image: ghcr.io/beyto1974/imapsmtpserver:latest`.

Then:
- Send test mail to `localhost:1025` (no auth) — e.g. `swaks --to a@b.test --server localhost:1025`
- Open `http://localhost:8025` for the web UI — pick an account from the
  dropdown to see its Inbox/Sent, or use "Compose"/"Reply" to send mail
  between accounts
- Point a mail client at `localhost:1143`, logging in as the account address
  you want to browse (any password) — `INBOX` and `Sent` are separate
  mailboxes

## Releases

Pushing a tag matching `v*.*.*` (e.g. `v0.1.0`) triggers
`.github/workflows/release.yml`, which first runs `go vet`/`go test`, then
(only if that passes):
- builds `imapsmtpserver` for linux/amd64, linux/arm64, windows/amd64,
  darwin/amd64 and darwin/arm64, and attaches the archives to a GitHub
  Release for that tag
- builds and pushes a multi-arch (`linux/amd64`, `linux/arm64`) Docker image
  to `ghcr.io/beyto1974/imapsmtpserver`, tagged with the version, the
  major.minor, and `latest` — see [Deploying with Docker](#deploying-with-docker)

## Layout

```
cmd/imapsmtpserver/   main.go (entrypoint), e2e_test.go
internal/store/       in-memory message store, per-account inbox/sent views
internal/mailparse/   RFC822 -> store.Message parsing
internal/smtpd/       SMTP server
internal/imapd/       IMAP server (per-account, backed by internal/store, supports APPEND)
internal/web/         HTTP API + static frontend (internal/web/static)
Dockerfile            multi-stage build (golang -> distroless static)
docker-compose.yml    maps ports 1025/1143/8025 to the host
```

## Notes for future work

- The IMAP backend only tracks the `\Seen` flag; other flag updates (e.g.
  `\Deleted`, `\Flagged`) are accepted but silently dropped since there's no
  persistence to back them.
- IMAP `APPEND` is supported (`CreateMessage`), letting mail clients file
  messages directly - e.g. a Sent copy after submitting over SMTP
  separately, or migrating old mail into INBOX. Since INBOX/Sent are
  filters over From/To rather than physical folders, appending forces the
  logged-in account into the right field (Sent: overwrites From; INBOX:
  adds the account as a recipient if missing) so the message is guaranteed
  to show up in the mailbox it was appended to. `CopyMessages` and mailbox
  management (create/delete/rename) remain unsupported. Because Sent is
  derived from the same store SMTP writes to, a mail client's normal habit
  of submitting over SMTP and then separately APPENDing an identical Sent
  copy would otherwise show up as a duplicate; appends whose Message-Id
  matches an already-stored message are recognized as "already filed"
  instead of stored again (`store.FindByMessageID`).
- The web frontend gets live updates via `GET /api/events` (SSE): the
  backend pushes an `update` event whenever the store changes, and the
  frontend refetches the current list in response. `EventSource` reconnects
  automatically on drop; polling is only used as a fallback if the browser
  doesn't support SSE.
- Accounts are inferred, not configured: an address becomes visible in the
  account dropdown / as an IMAP login only after it has appeared in a
  message's From or To. There's no address book or validation that a "to"
  address is well-formed beyond basic parsing.
- `POST /api/send` builds a plain-text-only RFC822 message; there's no rich
  text/HTML compose or attachment upload from the web UI yet.

## License

[MIT](LICENSE)
