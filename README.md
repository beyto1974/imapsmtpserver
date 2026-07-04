# ImapSmtpServer

A very simple dev-only SMTP/IMAP mail catcher with a web UI, in the spirit of
[Mailpit](https://github.com/axllent/mailpit). Written in Go. No auth, no
relaying, no persistence — everything lives in memory and is gone on restart.

## Status

Done:
- [x] `internal/store` — thread-safe in-memory message store (add/list/get/delete/clear, UID tracking for IMAP, `\Seen` flag)
- [x] `internal/mailparse` — parses raw RFC822 bytes into subject/from/to/date/text/html/attachments using `go-message/mail`
- [x] `internal/smtpd` — minimal SMTP server (`github.com/emersion/go-smtp`), accepts any sender/recipient, no auth required, parses each message and adds it to the store
- [x] `internal/imapd` — read-only IMAP server (`github.com/emersion/go-imap`), single hardcoded user (any username/password), single `INBOX` mailbox backed by `internal/store`
- [x] `internal/web` — REST API + embedded static frontend (list, view html/text/raw, download attachment, clear one, clear all), live updates over Server-Sent Events (`GET /api/events`)
- [x] `cmd/imapsmtpserver/main.go` — wires SMTP + IMAP + web servers together, graceful shutdown on SIGINT/SIGTERM
- [x] End-to-end test (`cmd/imapsmtpserver/e2e_test.go`): sends a mail via SMTP, checks it in the web API, fetches it via IMAP, clears it via the web API

## Running

```sh
go build ./...
go run ./cmd/imapsmtpserver
```

Then:
- Send test mail to `localhost:1025` (no auth) — e.g. `swaks --to a@b.test --server localhost:1025`
- Open `http://localhost:8025` for the web UI
- Point a mail client at `localhost:1143` (any username/password) to browse via IMAP

## Layout

```
cmd/imapsmtpserver/   main.go (entrypoint), e2e_test.go
internal/store/       in-memory message store
internal/mailparse/   RFC822 -> store.Message parsing
internal/smtpd/       SMTP server
internal/imapd/       IMAP server (read-only, backed by internal/store)
internal/web/         HTTP API + static frontend (internal/web/static)
```

## Notes for future work

- The IMAP backend only tracks the `\Seen` flag; other flag updates (e.g.
  `\Deleted`, `\Flagged`) are accepted but silently dropped since there's no
  persistence to back them.
- `CreateMessage`/`CopyMessages` on the IMAP mailbox return an error — mail
  only arrives via SMTP, this is a read-only mailbox by design.
- The web frontend gets live updates via `GET /api/events` (SSE): the
  backend pushes an `update` event whenever the store changes, and the
  frontend refetches `/api/messages` in response. `EventSource` reconnects
  automatically on drop; polling is only used as a fallback if the browser
  doesn't support SSE.
