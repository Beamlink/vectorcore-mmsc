package smpp

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/vectorcore/vectorcore-mmsc/internal/wappush"
)

func rawPDU(commandID, sequence uint32, body []byte) []byte {
	out := make([]byte, 16, 16+len(body))
	binary.BigEndian.PutUint32(out[0:], uint32(16+len(body)))
	binary.BigEndian.PutUint32(out[4:], commandID)
	binary.BigEndian.PutUint32(out[12:], sequence)
	return append(out, body...)
}

// OsmoMSC sends alert_notification when a subscriber becomes reachable; it must
// not end the session, and other unknown requests get a generic_nack.
func TestClientSurvivesAlertNotificationAndNacksUnknownRequests(t *testing.T) {
	t.Parallel()

	nacks := make(chan *PDU, 4)
	submits := make(chan *PDU, 1)
	serverConn, clientConn := net.Pipe()
	go func() {
		defer serverConn.Close()
		wrapped := NewConn(serverConn)
		for {
			pdu, err := wrapped.ReadPDU()
			if err != nil {
				return
			}
			switch pdu.CommandID {
			case CmdBindTransceiver:
				_ = wrapped.WritePDU(&PDU{CommandID: CmdBindTransceiverResp, SequenceNumber: pdu.SequenceNumber, SystemID: "smsc"})
				// alert_notification: source ton/npi/addr, esme ton/npi/addr.
				_, _ = serverConn.Write(rawPDU(CmdAlertNotification, 200, []byte("\x01\x01+12025550100\x00\x00\x00\x00")))
				// data_sm, which the client does not implement.
				_, _ = serverConn.Write(rawPDU(0x00000103, 201, []byte("\x00\x00\x00\x00\x00\x00\x00")))
			case CmdGenericNack:
				nacks <- pdu
			case CmdSubmitSM:
				submits <- pdu
				_ = wrapped.WritePDU(&PDU{CommandID: CmdSubmitSMResp, SequenceNumber: pdu.SequenceNumber, MessageID: "mid-1"})
			}
		}
	}()

	client := NewClient(Config{Host: "127.0.0.1", Port: 2775, SystemID: "mmsc", Password: "secret", BindMode: "transceiver"})
	client.SetDialFunc(func(context.Context, string, string) (net.Conn, error) { return clientConn, nil })
	defer client.Close()

	if err := client.SubmitWAPPush(context.Background(), "+12025550199", "+12025550100", wappush.WrapMMSPDU(make([]byte, 32))); err != nil {
		t.Fatalf("submit after alert_notification: %v", err)
	}
	select {
	case <-submits:
	case <-time.After(2 * time.Second):
		t.Fatal("no submit_sm reached the SMSC")
	}
	select {
	case nack := <-nacks:
		if nack.SequenceNumber != 201 || nack.CommandStatus != ESMERINVCMDID {
			t.Fatalf("unexpected nack: seq=%d status=%#x", nack.SequenceNumber, nack.CommandStatus)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unknown request was not nacked")
	}
	select {
	case nack := <-nacks:
		t.Fatalf("alert_notification must not be answered, got nack seq=%d", nack.SequenceNumber)
	default:
	}
}
