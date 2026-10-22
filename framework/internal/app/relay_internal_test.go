package app

import (
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"
)

// StartRefusingRelay is an SMTP server for the tests that answers reply —
// "550 5.1.1 …", "451 4.3.0 …" — to every RCPT: the mail is refused there.
// It is exported for the external suite's bounce tests, beside the internal
// ones that run an outbox on the fake database.
func StartRefusingRelay(t *testing.T, reply string) string {
	t.Helper()
	ln, lnErr := net.Listen("tcp", "127.0.0.1:0")
	if lnErr != nil {
		t.Fatal(lnErr)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Go(func() { refuse(c, reply) })
		}
	})
	t.Cleanup(func() {
		ln.Close()
		wg.Wait()
	})
	return ln.Addr().String()
}

// refuse speaks SMTP on c until QUIT, answering reply to RCPT.
func refuse(c net.Conn, reply string) {
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return
	}
	tp := textproto.NewConn(c)
	if err := tp.PrintfLine("220 relay.test ESMTP"); err != nil {
		return
	}
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb, _, _ := strings.Cut(line, " ")
		answer := "250 OK"
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			answer = "250 relay.test"
		case "RCPT":
			answer = reply
		case "QUIT":
			_ = tp.PrintfLine("221 bye")
			return
		}
		if err := tp.PrintfLine("%s", answer); err != nil {
			return
		}
	}
}
