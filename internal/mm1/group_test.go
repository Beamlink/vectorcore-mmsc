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

func TestRetrieveOmitsRetrievingHandset(t *testing.T) {
	cfg := config.Default()
	cfg.MM1.TrustMSISDNHeader = true
	cfg.Database.Driver = "sqlite"
	cfg.Database.DSN = t.TempDir() + "/mm1-self.db"
	cfg.Store.Backend = "filesystem"
	cfg.Store.Filesystem.Root = t.TempDir()

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
	server := NewServer(cfg, repo, contentStore, routing.NewEngine(repo), nil, nil, nil, nil)

	raw, err := mmspdu.Encode(mmspdu.NewRetrieveConf("txn-self", []mmspdu.Part{{ContentType: "text/plain", Data: []byte("hi")}}))
	if err != nil {
		t.Fatalf("encode content: %v", err)
	}
	path, err := contentStore.Put(ctx, "mid-self", bytes.NewReader(raw), int64(len(raw)), "application/vnd.wap.mms-message")
	if err != nil {
		t.Fatalf("store content: %v", err)
	}
	for _, msg := range []*message.Message{
		{ID: "mid-self", TransactionID: "txn-self", From: "9016399992", To: []string{"9820288882"},
			ContentPath: path, StoreID: path, Parts: []message.Part{{ContentType: "text/plain", Data: []byte("hi")}}},
		{ID: "mid-group", TransactionID: "txn-group", From: "9016399992", To: []string{"9820288882", "9910199993"},
			CC: []string{"9910199996"}, ContentPath: path, StoreID: path, Parts: []message.Part{{ContentType: "text/plain", Data: []byte("hi")}}},
	} {
		if err := repo.CreateMessage(ctx, msg); err != nil {
			t.Fatalf("create %s: %v", msg.ID, err)
		}
	}

	retrieve := func(id, self string) *mmspdu.PDU {
		req := httptest.NewRequest(http.MethodGet, "/mms/retrieve?id="+id, nil)
		req.Header.Set("X-MSISDN", self)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("retrieve %s: %d %s", id, rec.Code, rec.Body.String())
		}
		pdu, err := mmspdu.Decode(rec.Body.Bytes())
		if err != nil {
			t.Fatalf("decode %s: %v", id, err)
		}
		return pdu
	}

	if n := len(retrieve("mid-self", "9820288882").AddressValues(mmspdu.FieldTo)); n != 0 {
		t.Fatalf("1:1 retrieve listed %d To addresses, want 0 (self omitted)", n)
	}
	group := retrieve("mid-group", "+19820288882")
	if n := len(group.AddressValues(mmspdu.FieldTo)); n != 1 {
		t.Fatalf("group retrieve To count %d, want 1", n)
	}
	if n := len(group.AddressValues(mmspdu.FieldCC)); n != 1 {
		t.Fatalf("group retrieve Cc count %d, want 1", n)
	}
}

func TestSameNumber(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"9820288882", "9820288882", true},
		{"+19820288882/TYPE=PLMN", "9820288882", true},
		{"9820288882", "9820288883", false},
		{"123", "0123", false},
		{"9820288882", "", false},
	} {
		if got := sameNumber(tc.a, tc.b); got != tc.want {
			t.Errorf("sameNumber(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
