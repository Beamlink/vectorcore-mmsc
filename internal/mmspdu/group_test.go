package mmspdu

import (
	"bytes"
	"testing"
)

func addressTexts(t *testing.T, pdu *PDU, field byte) []string {
	t.Helper()
	var out []string
	for _, value := range pdu.AddressValues(field) {
		text, err := value.Text()
		if err != nil {
			t.Fatalf("address text: %v", err)
		}
		out = append(out, text)
	}
	return out
}

func TestRepeatedAddressHeadersRoundTrip(t *testing.T) {
	pdu := NewSendReqWithParts("txn-group", "+12025550100",
		[]string{"+12025550101", "+12025550102", "+12025550103"},
		[]Part{{ContentType: "text/plain", Data: []byte("hi all")}})
	pdu.AddAddress(FieldCC, "+12025550104")
	pdu.AddAddress(FieldBCC, "+12025550105")

	encoded, err := Encode(pdu)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	to := addressTexts(t, decoded, FieldTo)
	if len(to) != 3 || to[0] != "+12025550101/TYPE=PLMN" && to[0] != "+12025550101" {
		t.Fatalf("unexpected to list: %#v", to)
	}
	if got := addressTexts(t, decoded, FieldCC); len(got) != 1 {
		t.Fatalf("unexpected cc list: %#v", got)
	}
	if got := addressTexts(t, decoded, FieldBCC); len(got) != 1 {
		t.Fatalf("unexpected bcc list: %#v", got)
	}

	// Re-encoding a decoded PDU must reproduce each repeat exactly once.
	again, err := Encode(decoded)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if !bytes.Equal(encoded, again) {
		t.Fatalf("re-encode changed bytes:\n%x\n%x", encoded, again)
	}
	if n := bytes.Count(again, []byte("+1202555010")); n != 6 {
		t.Fatalf("expected 6 addresses (from + 5 recipients), found %d", n)
	}
}

func TestNonRepeatableHeaderLastValueWins(t *testing.T) {
	// Subject is not repeatable: keep the upstream last-value behavior.
	raw := []byte{
		0x80 | FieldMessageType, MsgTypeSendReq,
		0x80 | FieldSubject, 'a', 0x00,
		0x80 | FieldSubject, 'b', 0x00,
	}
	pdu, err := Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(pdu.Extra) != 0 {
		t.Fatalf("subject should not be kept as extra: %#v", pdu.Extra)
	}
	if text, _ := pdu.Headers[FieldSubject].Text(); text != "b" {
		t.Fatalf("expected last subject, got %q", text)
	}
}
