package mm1

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vectorcore/vectorcore-mmsc/internal/config"
	"github.com/vectorcore/vectorcore-mmsc/internal/db"
	"github.com/vectorcore/vectorcore-mmsc/internal/message"
	"github.com/vectorcore/vectorcore-mmsc/internal/mmspdu"
	"github.com/vectorcore/vectorcore-mmsc/internal/routing"
	"github.com/vectorcore/vectorcore-mmsc/internal/store"
)

func TestGroupSendNotifiesEveryRecipientAndRetrieveListsGroup(t *testing.T) {
	cfg := config.Default()
	cfg.Database.Driver = "sqlite"
	cfg.Database.DSN = t.TempDir() + "/mm1-group.db"
	cfg.Store.Backend = "filesystem"
	cfg.Store.Filesystem.Root = t.TempDir()
	cfg.MM4.Hostname = "mmsc.example.net"

	ctx := context.Background()
	repo, err := db.Open(ctx, db.OpenOptions{Driver: cfg.Database.Driver, DSN: cfg.Database.DSN})
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer repo.Close()
	runMM1TestMigrations(t, repo)

	contentStore, err := store.New(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer contentStore.Close()

	pushSender := &fakePushSender{}
	server := NewServer(cfg, repo, contentStore, routing.NewEngine(repo), nil, nil, nil, nil)
	server.handler = NewMOHandler(cfg, repo, contentStore, routing.NewEngine(repo), pushSender, nil, nil, nil)

	reqPDU := mmspdu.NewSendReqWithParts("txn-group", "+12025550100",
		[]string{"+12025550101/TYPE=PLMN", "+12025550102/TYPE=PLMN"},
		[]mmspdu.Part{{ContentType: "text/plain", Data: []byte("hi all")}})
	reqPDU.AddAddress(mmspdu.FieldCC, "+12025550103/TYPE=PLMN")
	reqPDU.AddAddress(mmspdu.FieldBCC, "+12025550104/TYPE=PLMN")
	reqBody, err := mmspdu.Encode(reqPDU)
	if err != nil {
		t.Fatalf("encode req: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/vnd.wap.mms-message")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("send status %d: %s", rec.Code, rec.Body.String())
	}

	var stored *message.Message
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		messages, err := repo.ListMessages(ctx, db.MessageFilter{Limit: 10})
		if err != nil {
			t.Fatalf("list messages: %v", err)
		}
		if len(messages) == 1 && messages[0].Status == message.StatusDelivering {
			stored = &messages[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if stored == nil {
		t.Fatal("message never reached delivering")
	}
	if strings.Join(stored.To, ",") != "+12025550101,+12025550102" ||
		strings.Join(stored.CC, ",") != "+12025550103" ||
		strings.Join(stored.BCC, ",") != "+12025550104" {
		t.Fatalf("stored recipients to=%v cc=%v bcc=%v", stored.To, stored.CC, stored.BCC)
	}
	if got := strings.Join(pushSender.recipients, ","); got != "+12025550101,+12025550102,+12025550103,+12025550104" {
		t.Fatalf("pushed to %q", got)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/mms/retrieve?id="+stored.ID, nil)
	getRec := httptest.NewRecorder()
	server.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("retrieve status %d: %s", getRec.Code, getRec.Body.String())
	}
	conf, err := mmspdu.Decode(getRec.Body.Bytes())
	if err != nil {
		t.Fatalf("decode retrieve-conf: %v", err)
	}
	if n := len(conf.AddressValues(mmspdu.FieldTo)); n != 2 {
		t.Fatalf("retrieve-conf To count %d, want 2", n)
	}
	if n := len(conf.AddressValues(mmspdu.FieldCC)); n != 1 {
		t.Fatalf("retrieve-conf Cc count %d, want 1", n)
	}
	if n := len(conf.AddressValues(mmspdu.FieldBCC)); n != 0 {
		t.Fatalf("retrieve-conf leaked Bcc (%d)", n)
	}
	if bytes.Contains(getRec.Body.Bytes(), []byte("+12025550104")) {
		t.Fatal("retrieve-conf contains the Bcc address")
	}
}

func TestTrustedMSISDNHeaderOverridesPDUFrom(t *testing.T) {
	for _, trust := range []bool{false, true} {
		cfg := config.Default()
		cfg.MM1.TrustMSISDNHeader = trust
		handler := &MOHandler{cfg: cfg}
		pdu := mmspdu.NewSendReq("txn", "+15550000000/TYPE=PLMN", []string{"+12025550101"})
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("X-MSISDN", "9910199993")
		got, err := handler.senderAddressFromRequest(req, pdu)
		if err != nil {
			t.Fatalf("trust=%v: %v", trust, err)
		}
		want := "+15550000000"
		if trust {
			want = "9910199993"
		}
		if got != want {
			t.Fatalf("trust=%v: sender %q, want %q", trust, got, want)
		}
	}
}
