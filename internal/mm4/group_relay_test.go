package mm4

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/vectorcore/vectorcore-mmsc/internal/db"
	"github.com/vectorcore/vectorcore-mmsc/internal/message"
)

func relayGroupMessage(t *testing.T, msg *message.Message) (gotTo []string, gotMsg string) {
	t.Helper()

	repo := newMM4Repo(t)
	if err := repo.UpsertMM4Peer(context.Background(), db.MM4Peer{
		Domain: "peer.example.net", SMTPHost: "smtp.peer.example.net", SMTPPort: 2525, Active: true,
	}); err != nil {
		t.Fatalf("upsert peer: %v", err)
	}
	outbound := NewOutbound(NewPeerRouter(repo), "mmsc.example.net")
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	outbound.dialFn = func(context.Context, string, string) (net.Conn, error) { return clientConn, nil }
	outbound.sleepFn = func(time.Duration) {}
	done := make(chan struct{})
	go fakeMM4SMTPServer(t, serverConn, false, "", "", func(_ string, to []string, data []byte) {
		gotTo = append([]string(nil), to...)
		gotMsg = string(data)
		close(done)
	})
	if err := outbound.Send(context.Background(), msg); err != nil {
		t.Fatalf("send: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the relay")
	}
	return gotTo, gotMsg
}

func TestOutboundRelaysCcAndBccWithoutRevealingBcc(t *testing.T) {
	t.Parallel()

	gotTo, data := relayGroupMessage(t, &message.Message{
		ID: "mid-group", TransactionID: "txn-group", From: "+12025550100",
		To:    []string{"to@peer.example.net"},
		CC:    []string{"cc@peer.example.net"},
		BCC:   []string{"bcc@peer.example.net"},
		Parts: []message.Part{{ContentType: "text/plain", Data: []byte("body")}},
	})
	if strings.Join(gotTo, ",") != "to@peer.example.net,cc@peer.example.net,bcc@peer.example.net" {
		t.Fatalf("SMTP recipients = %v, want To, Cc and Bcc", gotTo)
	}
	if !strings.Contains(data, "cc@peer.example.net") || !strings.Contains(strings.ToLower(data), "x-mms-cc:") {
		t.Fatalf("Cc missing from the visible headers:\n%s", data)
	}
	if strings.Contains(data, "bcc@peer.example.net") {
		t.Fatalf("Bcc leaked into the message:\n%s", data)
	}
}

func TestOutboundRelaysBccOnlyMessage(t *testing.T) {
	t.Parallel()

	gotTo, data := relayGroupMessage(t, &message.Message{
		ID: "mid-bcc", TransactionID: "txn-bcc", From: "+12025550100",
		BCC:   []string{"bcc@peer.example.net"},
		Parts: []message.Part{{ContentType: "text/plain", Data: []byte("body")}},
	})
	if strings.Join(gotTo, ",") != "bcc@peer.example.net" {
		t.Fatalf("SMTP recipients = %v", gotTo)
	}
	if strings.Contains(data, "bcc@peer.example.net") {
		t.Fatalf("Bcc leaked into the message:\n%s", data)
	}
}

func TestEnvelopeRoundTripKeepsCc(t *testing.T) {
	t.Parallel()

	raw, err := EncodeEnvelope(&message.Message{
		ID: "mid", TransactionID: "txn", From: "+12025550100",
		To: []string{"+12025550101"}, CC: []string{"+12025550102"}, BCC: []string{"+12025550103"},
		Parts: []message.Part{{ContentType: "text/plain", Data: []byte("body")}},
	}, "mmsc.example.net")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if strings.Contains(string(raw), "12025550103") {
		t.Fatalf("Bcc leaked into the envelope:\n%s", raw)
	}
	decoded, _, err := DecodeEnvelopeWithMeta(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if strings.Join(decoded.To, ",") != "+12025550101" || strings.Join(decoded.CC, ",") != "+12025550102" {
		t.Fatalf("decoded To=%v Cc=%v", decoded.To, decoded.CC)
	}
}

func TestEnvelopeOnlyRecipientsAreBcc(t *testing.T) {
	t.Parallel()

	got := envelopeOnlyRecipients(
		[]string{"+12025550101/TYPE=PLMN", "+12025550102", "+12025550103"},
		[]string{"+12025550101"}, []string{"+12025550102"})
	if strings.Join(got, ",") != "+12025550103" {
		t.Fatalf("envelopeOnlyRecipients = %v", got)
	}
}
