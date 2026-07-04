// Command imapsmtpserver runs an in-memory SMTP/IMAP mail catcher with a web
// UI, in the spirit of Mailpit. Dev tool only: no auth, no relaying, no
// persistence.
package main

import (
	"context"
	"flag"
	"fmt"
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
	host := flag.String("host", "127.0.0.1", "address to bind the SMTP/IMAP/web servers to (use 0.0.0.0 to accept connections from other hosts, e.g. in Docker)")
	smtpPort := flag.Int("smtp-port", 1025, "SMTP server port")
	imapPort := flag.Int("imap-port", 1143, "IMAP server port")
	webPort := flag.Int("web-port", 8025, "web UI/API port")
	flag.Parse()

	st := store.New()

	smtpAddr := fmt.Sprintf("%s:%d", *host, *smtpPort)
	// The web UI's compose/reply feature submits to the SMTP server from
	// within this same process, regardless of which host/interface it's
	// bound to for external clients - so it always dials loopback.
	smtpLoopbackAddr := fmt.Sprintf("127.0.0.1:%d", *smtpPort)

	smtpSrv := smtpd.New(smtpAddr, st)
	imapSrv := imapd.New(fmt.Sprintf("%s:%d", *host, *imapPort), st)
	webSrv := web.New(fmt.Sprintf("%s:%d", *host, *webPort), st, smtpLoopbackAddr)

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
