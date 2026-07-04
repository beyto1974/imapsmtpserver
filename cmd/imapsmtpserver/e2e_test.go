package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"testing"
	"time"

	imapclient "github.com/emersion/go-imap/client"

	"imapsmtpserver/internal/imapd"
	"imapsmtpserver/internal/smtpd"
	"imapsmtpserver/internal/store"
	"imapsmtpserver/internal/web"
)

type testServers struct {
	st      *store.Store
	baseURL string
	smtpLn  net.Listener
	imapLn  net.Listener
}

func startServers(t *testing.T) testServers {
	t.Helper()

	st := store.New()

	smtpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	imapLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	webLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	smtpSrv := smtpd.New(smtpLn.Addr().String(), st)
	imapSrv := imapd.New(imapLn.Addr().String(), st)
	webSrv := web.New(webLn.Addr().String(), st, smtpLn.Addr().String())

	go smtpSrv.Serve(smtpLn)
	go imapSrv.Serve(imapLn)
	go webSrv.Serve(webLn)
	t.Cleanup(func() {
		smtpSrv.Close()
		imapSrv.Close()
		webSrv.Close()
	})

	return testServers{st: st, baseURL: "http://" + webLn.Addr().String(), smtpLn: smtpLn, imapLn: imapLn}
}

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decode %s: %v (body: %s)", url, err, body)
	}
}

// waitFor polls check until it returns true or the deadline passes, failing
// the test if it never does. Needed because sending mail (SMTP submission,
// even to the server's own listener) completes asynchronously with respect
// to when it lands in the store.
func waitFor(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if check() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("condition not met before deadline")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestEndToEnd sends a mail via SMTP, checks it shows up in the web API,
// fetches it via IMAP (logged in as the recipient account), then clears it
// via the web API.
func TestEndToEnd(t *testing.T) {
	srv := startServers(t)

	const rawMsg = "From: sender@example.test\r\n" +
		"To: recipient@example.test\r\n" +
		"Subject: Hello from e2e test\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"This is the test body.\r\n"

	if err := smtp.SendMail(srv.smtpLn.Addr().String(), nil, "sender@example.test",
		[]string{"recipient@example.test"}, []byte(rawMsg)); err != nil {
		t.Fatalf("SendMail: %v", err)
	}

	var messages []struct {
		ID      string `json:"id"`
		Subject string `json:"subject"`
	}
	waitFor(t, func() bool {
		getJSON(t, srv.baseURL+"/api/messages", &messages)
		return len(messages) > 0
	})
	if len(messages) != 1 {
		t.Fatalf("expected 1 message in web API, got %d", len(messages))
	}
	if messages[0].Subject != "Hello from e2e test" {
		t.Fatalf("unexpected subject: %q", messages[0].Subject)
	}

	// IMAP: log in as the recipient account, select INBOX, fetch the message.
	c, err := imapclient.Dial(srv.imapLn.Addr().String())
	if err != nil {
		t.Fatalf("imap dial: %v", err)
	}
	defer c.Logout()

	if err := c.Login("recipient@example.test", "anypass"); err != nil {
		t.Fatalf("imap login: %v", err)
	}
	mbox, err := c.Select("INBOX", false)
	if err != nil {
		t.Fatalf("imap select: %v", err)
	}
	if mbox.Messages != 1 {
		t.Fatalf("expected 1 message via IMAP, got %d", mbox.Messages)
	}

	// Web API: clear the message and verify it's gone.
	req, err := http.NewRequest(http.MethodDelete, srv.baseURL+"/api/messages/"+messages[0].ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE message: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE message: status %d", resp.StatusCode)
	}
	if srv.st.Count() != 0 {
		t.Fatalf("expected store to be empty after clear, got %d", srv.st.Count())
	}
}

// TestMultiAccountSendAndReply drives the web UI's compose/reply feature:
// alice sends bob a message via POST /api/send (which loops back through the
// server's own SMTP port), bob replies, and each account's IMAP INBOX/Sent
// mailboxes only ever show that account's own mail.
func TestMultiAccountSendAndReply(t *testing.T) {
	srv := startServers(t)

	const alice = "alice@example.test"
	const bob = "bob@example.test"

	send := func(from, to, subject, text, inReplyTo string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{
			"from": from, "to": to, "subject": subject, "text": text, "inReplyTo": inReplyTo,
		})
		resp, err := http.Post(srv.baseURL+"/api/send", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /api/send: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("POST /api/send: status %d, body %s", resp.StatusCode, b)
		}
	}

	type summary struct {
		ID      string `json:"id"`
		Subject string `json:"subject"`
	}

	// Alice sends the first message to Bob.
	send(alice, bob, "Hello Bob", "Hi Bob, how are you?", "")

	var accounts []string
	waitFor(t, func() bool {
		getJSON(t, srv.baseURL+"/api/accounts", &accounts)
		return len(accounts) == 2
	})

	var bobInbox []summary
	waitFor(t, func() bool {
		getJSON(t, srv.baseURL+"/api/accounts/"+bob+"/messages?folder=inbox", &bobInbox)
		return len(bobInbox) == 1
	})
	if bobInbox[0].Subject != "Hello Bob" {
		t.Fatalf("unexpected subject in bob's inbox: %q", bobInbox[0].Subject)
	}

	var aliceSent []summary
	getJSON(t, srv.baseURL+"/api/accounts/"+alice+"/messages?folder=sent", &aliceSent)
	if len(aliceSent) != 1 {
		t.Fatalf("expected 1 message in alice's sent folder, got %d", len(aliceSent))
	}

	// Bob replies to Alice's message. The reply UI prefills "to" from the
	// original message's "from" field, which mailparse/go-message render
	// wrapped in angle brackets (e.g. "<alice@example.test>") even with no
	// display name - the send handler must tolerate that rather than
	// double-wrapping it when it builds the SMTP RCPT command.
	var bobInboxDetail struct {
		From string `json:"from"`
	}
	getJSON(t, srv.baseURL+"/api/messages/"+bobInbox[0].ID, &bobInboxDetail)
	if bobInboxDetail.From == alice {
		t.Fatalf("expected original from field to be bracket-wrapped by go-message, got %q", bobInboxDetail.From)
	}
	send(bob, bobInboxDetail.From, "Re: Hello Bob", "Doing great, thanks!", bobInbox[0].ID)

	var aliceInbox []summary
	waitFor(t, func() bool {
		getJSON(t, srv.baseURL+"/api/accounts/"+alice+"/messages?folder=inbox", &aliceInbox)
		return len(aliceInbox) == 1
	})
	if aliceInbox[0].Subject != "Re: Hello Bob" {
		t.Fatalf("unexpected subject in alice's inbox: %q", aliceInbox[0].Subject)
	}

	// IMAP: each account only sees its own mail, split into INBOX/Sent.
	loginAndCount := func(account, mailbox string) uint32 {
		t.Helper()
		c, err := imapclient.Dial(srv.imapLn.Addr().String())
		if err != nil {
			t.Fatalf("imap dial: %v", err)
		}
		defer c.Logout()
		if err := c.Login(account, "anypass"); err != nil {
			t.Fatalf("imap login as %s: %v", account, err)
		}
		mbox, err := c.Select(mailbox, false)
		if err != nil {
			t.Fatalf("imap select %s for %s: %v", mailbox, account, err)
		}
		return mbox.Messages
	}

	if n := loginAndCount(bob, "INBOX"); n != 1 {
		t.Fatalf("bob INBOX: expected 1 message, got %d", n)
	}
	if n := loginAndCount(bob, "Sent"); n != 1 {
		t.Fatalf("bob Sent: expected 1 message, got %d", n)
	}
	if n := loginAndCount(alice, "INBOX"); n != 1 {
		t.Fatalf("alice INBOX: expected 1 message, got %d", n)
	}
	if n := loginAndCount(alice, "Sent"); n != 1 {
		t.Fatalf("alice Sent: expected 1 message, got %d", n)
	}
	if n := loginAndCount("nobody@example.test", "INBOX"); n != 0 {
		t.Fatalf("unrelated account INBOX: expected 0 messages, got %d", n)
	}
}
