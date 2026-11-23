package scheduler

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/smartblack24/email-script/fwd-email-outreach/internal/config"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/database"
)

func TestClaimAndRecoverDuplicatePrevention(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	job := database.FollowupJob{
		CampaignID:          "c1",
		ContactID:           1,
		CampaignRecipientID: 1,
		Step:                1,
		ScheduledAt:         time.Now().UTC().Add(-time.Minute),
		Status:              "pending",
	}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}

	claimed, err := ClaimDueJobs(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed=%d", len(claimed))
	}
	claimed2, err := ClaimDueJobs(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed2) != 0 {
		t.Fatal("job claimed twice")
	}

	// Stale recovery
	past := time.Now().UTC().Add(-time.Hour)
	_ = db.Model(&database.FollowupJob{}).Where("id = ?", claimed[0].ID).Update("claimed_at", past).Error
	n, err := RecoverStaleClaims(db, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("recovered=%d", n)
	}
	claimed3, err := ClaimDueJobs(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed3) != 1 {
		t.Fatal("expected reclaimed after recovery")
	}
}

func TestScheduleFollowupsNoDuplicates(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{FollowupEnabled: true, FollowupDays: []int{0, 4, 9}}
	now := time.Now().UTC()
	if err := ScheduleFollowups(db, cfg, "c1", 1, 1, now); err != nil {
		t.Fatal(err)
	}
	if err := ScheduleFollowups(db, cfg, "c1", 1, 1, now); err != nil {
		t.Fatal(err)
	}
	var count int64
	_ = db.Model(&database.FollowupJob{}).Count(&count).Error
	if count != 2 {
		t.Fatalf("count=%d want 2", count)
	}
}
