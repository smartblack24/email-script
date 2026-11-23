package sender

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/smartblack24/email-script/fwd-email-outreach/internal/config"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/database"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/gmail"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/scheduler"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/suppression"
	tpl "github.com/smartblack24/email-script/fwd-email-outreach/internal/templates"
)

func testCfg(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv("DRY_RUN", "false")
	t.Setenv("DAILY_SEND_LIMIT", "50")
	t.Setenv("HOURLY_SEND_LIMIT", "20")
	t.Setenv("MIN_SEND_INTERVAL_SECONDS", "0")
	t.Setenv("SEND_START_HOUR", "0")
	t.Setenv("SEND_END_HOUR", "23")
	t.Setenv("SEND_TIMEZONE", "UTC")
	t.Setenv("FOLLOWUP_DAYS", "0,4,9")
	t.Setenv("FOLLOWUP_ENABLED", "true")
	t.Setenv("SENDER_NAME", "Tester")
	t.Setenv("SENDER_EMAIL", "tester@fwd.example")
	t.Setenv("BUSINESS_NAME", "FWD Consulting Group")
	t.Setenv("BUSINESS_ADDRESS", "123 Test St")
	t.Setenv("OPT_OUT_EMAIL", "optout@fwd.example")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func testTemplates(t *testing.T) *tpl.Renderer {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"initial.txt":   "Subject: Hello {{.FirstName}}\n\nBody {{.Email}}\n",
		"followup1.txt": "Subject: FU1 {{.FirstName}}\n\nFollow 1\n",
		"followup2.txt": "Subject: FU2 {{.FirstName}}\n\nFollow 2\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := tpl.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDryRunSafetyDoesNotCallGmail(t *testing.T) {
	cfg := testCfg(t)
	dbPath := filepath.Join(t.TempDir(), "t.db")
	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	c := database.Contact{
		FullName: "Ada Lovelace", FirstName: "Ada", Email: "ada@example.com", EmailNormalized: "ada@example.com",
		Approved: true, ImportTimestamp: time.Now().UTC(),
	}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	mock := gmail.NewMockClient("tester@fwd.example")
	svc := &Service{DB: db, Cfg: cfg, Mail: mock, Templates: testTemplates(t)}
	res, err := svc.SendInitial(context.Background(), SendOptions{CampaignName: "default", Limit: 5, Execute: false})
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || res.Sent != 1 {
		t.Fatalf("unexpected result %+v", res)
	}
	if mock.SendCount != 0 {
		t.Fatalf("gmail should not be called in dry-run, got %d", mock.SendCount)
	}
}

func TestExecuteSendsAndSchedulesFollowups(t *testing.T) {
	cfg := testCfg(t)
	dbPath := filepath.Join(t.TempDir(), "t.db")
	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	c := database.Contact{
		FullName: "Ada Lovelace", FirstName: "Ada", Email: "ada@example.com", EmailNormalized: "ada@example.com",
		Approved: true, ImportTimestamp: time.Now().UTC(),
	}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	mock := gmail.NewMockClient("tester@fwd.example")
	svc := &Service{DB: db, Cfg: cfg, Mail: mock, Templates: testTemplates(t)}
	res, err := svc.SendInitial(context.Background(), SendOptions{CampaignName: "default", Limit: 5, Execute: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 1 || mock.SendCount != 1 {
		t.Fatalf("sent=%d mock=%d details=%v", res.Sent, mock.SendCount, res.Details)
	}
	var jobs int64
	if err := db.Model(&database.FollowupJob{}).Count(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if jobs != 2 { // days 4 and 9
		t.Fatalf("followup jobs=%d want 2", jobs)
	}

	// Duplicate prevention across restarts
	res2, err := svc.SendInitial(context.Background(), SendOptions{CampaignName: "default", Limit: 5, Execute: true})
	if err != nil {
		t.Fatal(err)
	}
	if mock.SendCount != 1 {
		t.Fatalf("duplicate send occurred: mock=%d res2=%+v", mock.SendCount, res2)
	}
}

func TestSuppressionBlocksSend(t *testing.T) {
	cfg := testCfg(t)
	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	c := database.Contact{
		FullName: "Ada Lovelace", FirstName: "Ada", Email: "ada@example.com", EmailNormalized: "ada@example.com",
		Approved: true, ImportTimestamp: time.Now().UTC(),
	}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	if err := suppression.Add(db, "ada@example.com", "opt_out", "test"); err != nil {
		t.Fatal(err)
	}
	mock := gmail.NewMockClient("tester@fwd.example")
	svc := &Service{DB: db, Cfg: cfg, Mail: mock, Templates: testTemplates(t)}
	res, err := svc.SendInitial(context.Background(), SendOptions{CampaignName: "default", Limit: 5, Execute: true})
	if err != nil {
		t.Fatal(err)
	}
	if mock.SendCount != 0 {
		t.Fatal("suppressed contact was emailed")
	}
	if res.Sent != 0 {
		t.Fatalf("sent=%d", res.Sent)
	}
}

func TestRateLimitDefers(t *testing.T) {
	cfg := testCfg(t)
	cfg.HourlySendLimit = 1
	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{"a@example.com", "b@example.com"} {
		c := database.Contact{
			FullName: email, FirstName: "X", Email: email, EmailNormalized: email,
			Approved: true, ImportTimestamp: time.Now().UTC(),
		}
		if err := db.Create(&c).Error; err != nil {
			t.Fatal(err)
		}
	}
	mock := gmail.NewMockClient("tester@fwd.example")
	svc := &Service{DB: db, Cfg: cfg, Mail: mock, Templates: testTemplates(t)}
	res, err := svc.SendInitial(context.Background(), SendOptions{CampaignName: "default", Limit: 5, Execute: true})
	if err != nil {
		t.Fatal(err)
	}
	if mock.SendCount != 1 {
		t.Fatalf("expected 1 send due to hourly limit, got %d (%+v)", mock.SendCount, res)
	}
	if res.Deferred < 1 && res.Sent != 1 {
		t.Fatalf("expected deferral %+v", res)
	}
}

func TestFollowupEligibilityStopsOnReply(t *testing.T) {
	cfg := testCfg(t)
	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	c := database.Contact{
		FullName: "Ada Lovelace", FirstName: "Ada", Email: "ada@example.com", EmailNormalized: "ada@example.com",
		Approved: true, ImportTimestamp: time.Now().UTC(),
	}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	mock := gmail.NewMockClient("tester@fwd.example")
	svc := &Service{DB: db, Cfg: cfg, Mail: mock, Templates: testTemplates(t)}
	if _, err := svc.SendInitial(context.Background(), SendOptions{CampaignName: "default", Limit: 1, Execute: true}); err != nil {
		t.Fatal(err)
	}
	var rec database.CampaignRecipient
	if err := db.First(&rec).Error; err != nil {
		t.Fatal(err)
	}
	mock.SetReply(rec.ThreadID)

	// Make follow-up due now
	if err := db.Model(&database.FollowupJob{}).Where("step = ?", 1).Update("scheduled_at", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	jobs, err := scheduler.ClaimDueJobs(db, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) == 0 {
		t.Fatal("expected claimed job")
	}
	before := mock.SendCount
	if err := svc.SendFollowupJob(context.Background(), &jobs[0], false); err != nil {
		t.Fatal(err)
	}
	if mock.SendCount != before {
		t.Fatal("follow-up should not send after reply")
	}
	if err := db.First(&rec, rec.ID).Error; err != nil {
		t.Fatal(err)
	}
	if rec.Status != "replied" {
		t.Fatalf("status=%s", rec.Status)
	}
}

func TestAmbiguousReconcilePreventsDuplicate(t *testing.T) {
	cfg := testCfg(t)
	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	c := database.Contact{
		FullName: "Ada Lovelace", FirstName: "Ada", Email: "ada@example.com", EmailNormalized: "ada@example.com",
		Approved: true, ImportTimestamp: time.Now().UTC(),
	}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	mock := gmail.NewMockClient("tester@fwd.example")
	mock.AmbiguousNext = true
	svc := &Service{DB: db, Cfg: cfg, Mail: mock, Templates: testTemplates(t)}
	_, _ = svc.SendInitial(context.Background(), SendOptions{CampaignName: "default", Limit: 1, Execute: true})

	// Simulate that message actually landed
	idem := ""
	var attempt database.SendAttempt
	if err := db.First(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	idem = attempt.IdempotencyKey
	mock.Results[idem] = &gmail.SendResult{MessageID: "msg-reconciled", ThreadID: "thread-r"}

	// Reset recipient to pending so loadEligible would pick it — but idempotency should reconcile
	_ = db.Model(&database.CampaignRecipient{}).Where("contact_id = ?", c.ID).Updates(map[string]any{"status": "pending", "stop_reason": ""}).Error
	before := mock.SendCount
	res, err := svc.SendInitial(context.Background(), SendOptions{CampaignName: "default", Limit: 1, Execute: true})
	if err != nil {
		t.Fatal(err)
	}
	if mock.SendCount != before {
		t.Fatalf("reconcile should prevent resend: %+v", res)
	}
}

func TestLiveSendRequiresCompliance(t *testing.T) {
	t.Setenv("DRY_RUN", "false")
	t.Setenv("SENDER_NAME", "")
	t.Setenv("SENDER_EMAIL", "")
	t.Setenv("BUSINESS_ADDRESS", "")
	t.Setenv("OPT_OUT_EMAIL", "")
	t.Setenv("SEND_TIMEZONE", "UTC")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{DB: db, Cfg: cfg, Mail: gmail.NewMockClient("x@y.com"), Templates: testTemplates(t)}
	_, err = svc.SendInitial(context.Background(), SendOptions{CampaignName: "default", Limit: 1, Execute: true})
	if err == nil {
		t.Fatal("expected compliance validation error")
	}
}
