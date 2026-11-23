package scheduler

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/config"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ScheduleFollowups creates jobs for configured follow-up days (excluding day 0).
func ScheduleFollowups(db *gorm.DB, cfg *config.Config, campaignID string, contactID, recipientID uint, initialSentAt time.Time) error {
	if cfg == nil || !cfg.FollowupEnabled {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		for _, day := range cfg.FollowupDays {
			if day <= 0 {
				continue
			}
			step := followupStepForDay(cfg.FollowupDays, day)
			var existing database.FollowupJob
			err := tx.Where("campaign_recipient_id = ? AND step = ?", recipientID, step).First(&existing).Error
			if err == nil {
				continue
			}
			if err != gorm.ErrRecordNotFound {
				return err
			}
			job := database.FollowupJob{
				CampaignID:          campaignID,
				ContactID:           contactID,
				CampaignRecipientID: recipientID,
				Step:                step,
				ScheduledAt:         initialSentAt.Add(time.Duration(day) * 24 * time.Hour),
				Status:              "pending",
			}
			if err := tx.Create(&job).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func followupStepForDay(days []int, day int) int {
	step := 0
	for _, d := range days {
		if d <= 0 {
			continue
		}
		step++
		if d == day {
			return step
		}
	}
	return step
}

// ClaimDueJobs transaction-safely claims pending jobs due now.
func ClaimDueJobs(db *gorm.DB, limit int) ([]database.FollowupJob, error) {
	if limit <= 0 {
		limit = 10
	}
	token := uuid.NewString()
	now := time.Now().UTC()
	var claimed []database.FollowupJob

	err := db.Transaction(func(tx *gorm.DB) error {
		var jobs []database.FollowupJob
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("status IN ? AND scheduled_at <= ?", []string{"pending", "postponed"}, now).
			Order("scheduled_at asc").
			Limit(limit).
			Find(&jobs).Error; err != nil {
			return err
		}
		for _, j := range jobs {
			res := tx.Model(&database.FollowupJob{}).
				Where("id = ? AND status IN ?", j.ID, []string{"pending", "postponed"}).
				Updates(map[string]any{
					"status":      "claimed",
					"claimed_at":  now,
					"claim_token": token,
					"attempts":    gorm.Expr("attempts + 1"),
				})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 1 {
				j.Status = "claimed"
				j.ClaimToken = token
				claimed = append(claimed, j)
			}
		}
		return nil
	})
	return claimed, err
}

// CompleteJob marks a job done.
func CompleteJob(db *gorm.DB, job *database.FollowupJob) error {
	return db.Model(job).Updates(map[string]any{
		"status":     "done",
		"updated_at": time.Now().UTC(),
	}).Error
}

// PostponeJob returns a job to postponed with a future schedule bump.
func PostponeJob(db *gorm.DB, job *database.FollowupJob, reason string) error {
	return db.Model(job).Updates(map[string]any{
		"status":       "postponed",
		"scheduled_at": time.Now().UTC().Add(30 * time.Minute),
		"last_error":   reason,
		"claim_token":  "",
		"updated_at":   time.Now().UTC(),
	}).Error
}

// CancelFollowups cancels pending jobs for a recipient.
func CancelFollowups(db *gorm.DB, recipientID uint, reason string) error {
	return db.Model(&database.FollowupJob{}).
		Where("campaign_recipient_id = ? AND status IN ?", recipientID, []string{"pending", "postponed", "claimed"}).
		Updates(map[string]any{
			"status":     "cancelled",
			"last_error": reason,
			"updated_at": time.Now().UTC(),
		}).Error
}

// RecoverStaleClaims requeues claimed jobs that timed out (e.g. process crash).
func RecoverStaleClaims(db *gorm.DB, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan)
	res := db.Model(&database.FollowupJob{}).
		Where("status = ? AND claimed_at < ?", "claimed", cutoff).
		Updates(map[string]any{
			"status":      "pending",
			"claim_token": "",
			"last_error":  fmt.Sprintf("recovered stale claim before %s", cutoff.Format(time.RFC3339)),
		})
	return res.RowsAffected, res.Error
}
