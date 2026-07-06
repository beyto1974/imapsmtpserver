# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A dev-only SMTP/IMAP mail catcher with a web UI, in the spirit of Mailpit. No
auth, no relaying to real mail servers, no persistence — everything lives in
an in-memory `internal/store.Store` and is gone on restart. Any
sender/recipient is accepted; any IMAP username/password logs in.

## Commands

```sh
go build ./...                       # build everything
go run ./cmd/imapsmtpserver          # run (SMTP :1025, IMAP :1143, web :8025)
go run ./cmd/imapsmtpserver -smtp-port 2525 -imap-port 1144 -web-port 8080 -host 0.0.0.0
go test ./...                        # run all tests
go test ./cmd/imapsmtpserver -run TestEndToEnd -v          # single test
go test ./cmd/imapsmtpserver -run TestMultiAccountSendAndReply -v
go test ./cmd/imapsmtpserver -run TestIMAPAppend -v
go vet ./...
gofmt -l .                           # list files needing formatting (fix: gofmt -w <file>)
docker compose up --build            # containerized run, maps 1025/1143/8025 to host
```

There are no unit tests per package — all testing lives in
`cmd/imapsmtpserver/e2e_test.go`, which spins up real SMTP/IMAP/web servers
on ephemeral `127.0.0.1:0` ports and drives them with real clients
(`net/smtp`, `github.com/emersion/go-imap/client`, `net/http`). When adding
behavior, prefer extending these end-to-end tests over introducing mocks —
that's the established pattern here.

## Architecture

Everything shares one `*store.Store` (`internal/store`), instantiated once in
`cmd/imapsmtpserver/main.go` and passed into all three servers. There is no
other cross-component communication.

**Data flow:** SMTP → parse → store → (IMAP read | web read | web write-back
via SMTP loopback):

- `internal/smtpd` — accepts any SMTP mail (`AllowInsecureAuth`, no auth
  enforced), reads the raw RFC822 bytes per message, and hands them to
  `internal/mailparse.Parse`, which extracts subject/from/to/date/text/html/
  attachments/Message-Id/In-Reply-To into a `store.Message` (keeping the
  original raw bytes too, for IMAP/raw-view). The parsed message is added to
  the store.
- `internal/imapd` — a **per-account** IMAP server built on
  `github.com/emersion/go-imap`'s `backend` interfaces. The login username
  becomes the account (via `store.NormalizeAddress`); each account gets two
  mailboxes, `INBOX` (`store.Inbox(account)`, i.e. messages where the account
  is a recipient) and `Sent` (`store.Sent(account)`, i.e. messages where the
  account is the sender). Since these are filters over From/To rather than
  physical folders, `CreateMessage` (APPEND) has to force the logged-in
  account into the right field or the appended message wouldn't show up in
  the mailbox it was just appended to: Sent overwrites `From`, INBOX adds the
  account to `To` if it's not already there (preserving other recipients).
  `CopyMessages` and mailbox management (create/delete/rename) still return
  `errReadOnly` — no use case for them yet. Message fetch/search/flag logic is
  built with `github.com/emersion/go-imap/backend/backendutil` helpers
  operating on the stored raw bytes; only the `\Seen` flag persists (others
  are accepted and silently dropped). **Append dedup:** mail clients
  conventionally submit over SMTP and then separately APPEND an identical
  Sent copy for their own records - on a real server that populates a folder
  SMTP never touches, but here Sent is derived from the same store SMTP
  already wrote to, so that append would double the message. `CreateMessage`
  checks `store.FindByMessageID` first and treats a match as already filed
  (just applying `\Seen` if requested) instead of storing it again — this
  was a real bug, see git history.
- `internal/web` — a JSON REST API (`/api/messages`, `/api/accounts`,
  `/api/accounts/{address}/messages?folder=inbox|sent`, `/api/send`,
  `/api/events`) plus the embedded static frontend (`internal/web/static/`,
  via `go:embed`). Two behaviors worth knowing before touching this package:
  - **Accounts are inferred, not configured.** `store.Accounts()` is just
    the distinct set of normalized addresses seen across all messages'
    From/To. There's no account registry.
  - **`POST /api/send` (compose/reply) submits back through the server's own
    SMTP port** (`net/smtp.SendMail`), rather than writing to the store
    directly — so composed/replied mail flows through the exact same
    SMTP → mailparse → store path as external mail, and shows up correctly
    in both the sender's Sent view and the recipient's Inbox. Addresses are
    passed through `store.NormalizeAddress` before being handed to
    `smtp.SendMail`, because addresses read back out of the store are
    already bracket-formatted (e.g. `<alice@example.test>`) by
    `go-message/mail`, and re-wrapping an already-bracketed address breaks
    the SMTP `RCPT TO` command — this bit us once, see git history.
  - Live updates use Server-Sent Events (`GET /api/events`): `store.Store`
    has a `Subscribe()`/`notify()` pub-sub mechanism, and every
    `Add`/`Delete`/`Clear`/`SetSeen` notifies subscribers, which the SSE
    handler turns into an `update` event. The frontend's `EventSource`
    reconnects automatically on drop; plain polling is only a fallback for
    browsers without SSE support.
- `cmd/imapsmtpserver/main.go` — wires the three servers together.
  **`-host` vs. the SMTP loopback address are deliberately different**: the
  bind host (`-host`, default `127.0.0.1`) controls what external clients
  can reach (e.g. `0.0.0.0` in Docker), but the address the web server uses
  to submit composed/reply mail is *always* `127.0.0.1:<smtp-port>`, since
  that submission is same-process communication regardless of which
  interface external clients connect through.

## Frontend gotcha

`internal/web/static/style.css` has hit the same CSS bug twice: an ID
selector like `#compose-overlay { display: flex; }` has higher specificity
than the browser's built-in `[hidden] { display: none }` rule, so toggling
the `hidden` attribute via JS silently does nothing. Any new element that's
shown/hidden via the `hidden` attribute and also has an unconditional
`display` rule needs an explicit `#id[hidden] { display: none; }` override
alongside it.

## CI/release

`.github/workflows/release.yml` triggers on pushing a `v*.*.*` tag: runs
`go vet`/`go test` first, and only if that passes, cross-compiles
`imapsmtpserver` for linux/amd64, linux/arm64, windows/amd64, darwin/amd64,
darwin/arm64, and attaches the archives to a GitHub Release.
