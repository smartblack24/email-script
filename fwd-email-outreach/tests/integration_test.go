package tests

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/smartblack24/email-script/fwd-email-outreach/internal/campaign"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/config"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/contacts"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/database"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/gmail"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/importer"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/sender"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/suppression"
	tpl "github.com/smartblack24/email-script/fwd-email-outreach/internal/templates"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/tracker"
)

func TestEndToEndImportApproveDryRunExport(t *testing.T) {
	root := findProjectRoot(t)
	t.Setenv("DRY_RUN", "true")
	t.Setenv("SEND_TIMEZONE", "UTC")
	t.Setenv("SEND_START_HOUR", "0")
	t.Setenv("SEND_END_HOUR", "23")
	t.Setenv("MIN_SEND_INTERVAL_SECONDS", "0")
	t.Setenv("FOLLOWUP_ENABLED", "true")
	t.Setenv("SENDER_NAME", "Tester")
	t.Setenv("SENDER_EMAIL", "tester@fwd.example")
	t.Setenv("BUSINESS_NAME", "FWD Consulting Group")
	t.Setenv("BUSINESS_ADDRESS", "123 Test")
	t.Setenv("OPT_OUT_EMAIL", "opt@fwd.example")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "e2e.db")
	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	xlsx := filepath.Join(root, "sample-data", "contacts.xlsx")
	sum, err := importer.ImportFile(xlsx, importer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Valid < 1 {
		t.Fatalf("expected valid contacts from sample, got %+v", sum)
	}
	ins, _, err := contacts.PersistImport(db, sum, "sample import")
	if err != nil {
		t.Fatal(err)
	}
	if ins < 1 {
		t.Fatal("expected inserts")
	}

	approvedCSV := filepath.Join(root, "sample-data", "approved.csv")
	approved, _, err := contacts.ApproveFromCSV(db, approvedCSV)
	if err != nil {
		t.Fatal(err)
	}
	if approved < 1 {
		t.Fatal("expected approvals")
	}

	if err := suppression.Add(db, "alan@example.com", "test_opt_out", "test"); err != nil {
		t.Fatal(err)
	}

	renderer, err := tpl.Load(filepath.Join(root, "templates"))
	if err != nil {
		t.Fatal(err)
	}
	mock := gmail.NewMockClient("tester@fwd.example")
	// dry-run should not require DRY_RUN=false
	cfg.DryRun = true
	svc := &sender.Service{DB: db, Cfg: cfg, Mail: mock, Templates: renderer}
	res, err := svc.SendInitial(context.Background(), sender.SendOptions{
		CampaignName: "e2e",
		Limit:        20,
		Execute:      false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if mock.SendCount != 0 {
		t.Fatal("dry-run called gmail")
	}
	if res.Sent < 1 {
		t.Fatalf("expected dry-run records, got %+v", res)
	}

	camp, err := campaign.GetOrCreate(db, "e2e", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := campaign.Pause(db, camp.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := campaign.Resume(db, camp.Name); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "report.xlsx")
	if err := tracker.ExportExcel(db, out); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(out)
	if err != nil || st.Size() == 0 {
		t.Fatalf("export missing or empty: %v", err)
	}

	rep, err := tracker.Collect(db)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ContactsTotal < 1 || rep.Suppressed < 1 {
		t.Fatalf("bad status %+v", rep)
	}
}

func TestReplyTracking(t *testing.T) {
	t.Setenv("DRY_RUN", "false")
	t.Setenv("SEND_TIMEZONE", "UTC")
	t.Setenv("SEND_START_HOUR", "0")
	t.Setenv("SEND_END_HOUR", "23")
	t.Setenv("MIN_SEND_INTERVAL_SECONDS", "0")
	t.Setenv("SENDER_NAME", "Tester")
	t.Setenv("SENDER_EMAIL", "tester@fwd.example")
	t.Setenv("BUSINESS_ADDRESS", "123")
	t.Setenv("OPT_OUT_EMAIL", "opt@fwd.example")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	root := findProjectRoot(t)
	db, err := database.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	c := database.Contact{
		FullName: "Grace Hopper", FirstName: "Grace", Email: "grace@example.com", EmailNormalized: "grace@example.com",
		Approved: true, ImportTimestamp: time.Now().UTC(),
	}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	renderer, err := tpl.Load(filepath.Join(root, "templates"))
	if err != nil {
		t.Fatal(err)
	}
	mock := gmail.NewMockClient("tester@fwd.example")
	svc := &sender.Service{DB: db, Cfg: cfg, Mail: mock, Templates: renderer}
	if _, err := svc.SendInitial(context.Background(), sender.SendOptions{CampaignName: "r", Limit: 1, Execute: true}); err != nil {
		t.Fatal(err)
	}
	var rec database.CampaignRecipient
	if err := db.First(&rec).Error; err != nil {
		t.Fatal(err)
	}
	mock.SetReply(rec.ThreadID)
	n, err := tracker.CheckReplies(db, mock)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("replies=%d", n)
	}
}

func findProjectRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// tests/ -> project root
	root := filepath.Clean(filepath.Join(wd, ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		// already at root?
		if _, err2 := os.Stat(filepath.Join(wd, "go.mod")); err2 == nil {
			return wd
		}
		t.Fatalf("cannot locate project root from %s", wd)
	}
	return root
}
