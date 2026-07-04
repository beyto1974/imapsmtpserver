// Command imapsmtpserver runs an in-memory SMTP/IMAP mail catcher with a web
// UI, in the spirit of Mailpit. Dev tool only: no auth, no relaying, no
// persistence.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"imapsmtpserver/internal/imapd"
	"imapsmtpserver/internal/smtpd"
	"imapsmtpserver/internal/store"
	"imapsmtpserver/internal/web"
)

func main() {
	st := store.New()

	smtpSrv := smtpd.New("127.0.0.1:1025", st)
	imapSrv := imapd.New("127.0.0.1:1143", st)
	webSrv := web.New("127.0.0.1:8025", st)

	go func() {
		log.Printf("smtp: listening on %s", smtpSrv.Addr)
		if err := smtpSrv.ListenAndServe(); err != nil {
			log.Fatalf("smtp: %v", err)
		}
	}()

	go func() {
		log.Printf("imap: listening on %s", imapSrv.Addr)
		if err := imapSrv.ListenAndServe(); err != nil {
			log.Fatalf("imap: %v", err)
		}
	}()

	go func() {
		log.Printf("web: listening on http://%s", webSrv.Addr)
		if err := webSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("web: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	log.Println("shutting down")
	smtpSrv.Close()
	imapSrv.Close()
	webSrv.Shutdown(context.Background())
}
