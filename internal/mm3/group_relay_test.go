package mm3

import (
	"bufio"
	"context"
	"net"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/vectorcore/vectorcore-mmsc/internal/config"
	"github.com/vectorcore/vectorcore-mmsc/internal/db"
	"github.com/vectorcore/vectorcore-mmsc/internal/message"
)

// fakeSMTP accepts one message and reports its envelope recipients and body.
func fakeSMTP(t *testing.T, conn net.Conn, onData func(to []string, data string)) {
	defer conn.Close()
	reader := textproto.NewReader(bufio.NewReader(conn))
	say := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
	say("220 smtp.relay.example.net ESMTP")
	var to []string
	var data string
	for {
		line, err := reader.ReadLine()
		if err != nil {
			return
		}
		switch {
		case strings.HasPrefix(line, "EHLO "):
			say("250 smtp.relay.example.net")
		case strings.HasPrefix(line, "RCPT TO:"):
			to = append(to, strings.Trim(strings.TrimPrefix(line, "RCPT TO:"), "<>"))
			say("250 OK")
		case line == "DATA":
			say("354 go")
			block, err := reader.ReadDotBytes()
			if err != nil {
				return
			}
			data = string(block)
			say("250 OK")
		case line == "QUIT":
			say("221 Bye")
			onData(to, data)
			return
		default:
			say("250 OK")
		}
	}
}

func TestEncodeEnvelopeListsToAndCcButNeverBcc(t *testing.T) {
	t.Parallel()

	body, _, err := EncodeEnvelope(&message.Message{
		ID: "mid", From: "+12025550100",
		To:    []string{"to@example.net"},
		CC:    []string{"cc@example.net"},
		BCC:   []string{"bcc@example.net"},
		Parts: []message.Part{{ContentType: "text/plain", Data: []byte("hi")}},
	}, db.MM3Relay{DefaultSenderDomain: "mmsc.example.net"}, "mmsc.example.net")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "To: to@example.net\r\n") || !strings.Contains(text, "Cc: cc@example.net\r\n") {
		t.Fatalf("missing To/Cc headers:\n%s", text)
	}
	if strings.Contains(text, "bcc@example.net") {
		t.Fatalf("Bcc leaked into the message:\n%s", text)
	}
}

func TestOutboundRelaysToCcAndBccRecipients(t *testing.T) {
	t.Parallel()

	runtime := config.NewRuntimeStore()
	runtime.Replace(config.RuntimeSnapshot{MM3Relay: &db.MM3Relay{
		Enabled: true, SMTPHost: "smtp.relay.example.net", SMTPPort: 25, DefaultSenderDomain: "mmsc.example.net",
	}})
	sender := NewOutbound(runtime, "mmsc.example.net")
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	sender.dialFn = func(context.Context, string, string) (net.Conn, error) { return clientConn, nil }

	type result struct {
		to   []string
		data string
	}
	got := make(chan result, 1)
	go fakeSMTP(t, serverConn, func(to []string, data string) { got <- result{to, data} })

	err := sender.Send(context.Background(), &message.Message{
		ID: "mid-group", From: "+12025550100",
		To:    []string{"to@example.net"},
		CC:    []string{"cc@example.net"},
		BCC:   []string{"bcc@example.net"},
		Parts: []message.Part{{ContentType: "text/plain", Data: []byte("hi")}},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	select {
	case r := <-got:
		if strings.Join(r.to, ",") != "to@example.net,cc@example.net,bcc@example.net" {
			t.Fatalf("SMTP recipients = %v, want To, Cc and Bcc", r.to)
		}
		if strings.Contains(r.data, "bcc@example.net") {
			t.Fatalf("Bcc leaked into the message:\n%s", r.data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the relay")
	}
}
