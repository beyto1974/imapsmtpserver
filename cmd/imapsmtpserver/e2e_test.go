package main

import (
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

// TestEndToEnd sends a mail via SMTP, checks it shows up in the web API,
// fetches it via IMAP, then clears it via the web API.
func TestEndToEnd(t *testing.T) {
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
	webSrv := web.New(webLn.Addr().String(), st)

	go smtpSrv.Serve(smtpLn)
	go imapSrv.Serve(imapLn)
	go webSrv.Serve(webLn)
	defer smtpSrv.Close()
	defer imapSrv.Close()
	defer webSrv.Close()

	const rawMsg = "From: sender@example.test\r\n" +
		"To: recipient@example.test\r\n" +
		"Subject: Hello from e2e test\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"This is the test body.\r\n"

	if err := smtp.SendMail(smtpLn.Addr().String(), nil, "sender@example.test",
		[]string{"recipient@example.test"}, []byte(rawMsg)); err != nil {
		t.Fatalf("SendMail: %v", err)
	}

	// The web UI: message should show up in the list.
	baseURL := "http://" + webLn.Addr().String()
	var messages []struct {
		ID      string `json:"id"`
		Subject string `json:"subject"`
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := http.Get(baseURL + "/api/messages")
		if err != nil {
			t.Fatalf("GET /api/messages: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err := json.Unmarshal(body, &messages); err != nil {
			t.Fatalf("decode messages: %v", err)
		}
		if len(messages) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message in web API, got %d", len(messages))
	}
	if messages[0].Subject != "Hello from e2e test" {
		t.Fatalf("unexpected subject: %q", messages[0].Subject)
	}

	// IMAP: log in with arbitrary credentials, select INBOX, fetch the message.
	c, err := imapclient.Dial(imapLn.Addr().String())
	if err != nil {
		t.Fatalf("imap dial: %v", err)
	}
	defer c.Logout()

	if err := c.Login("anyuser", "anypass"); err != nil {
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
	req, err := http.NewRequest(http.MethodDelete, baseURL+"/api/messages/"+messages[0].ID, nil)
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
	if st.Count() != 0 {
		t.Fatalf("expected store to be empty after clear, got %d", st.Count())
	}
}
