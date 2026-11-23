package sender

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/smartblack24/email-script/fwd-email-outreach/internal/campaign"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/config"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/database"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/gmail"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/scheduler"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/suppression"
	tpl "github.com/smartblack24/email-script/fwd-email-outreach/internal/templates"
	"gorm.io/gorm"
)

// Service orchestrates throttled, idempotent sends.
type Service struct {
	DB       *gorm.DB
	Cfg      *config.Config
	Mail     gmail.MailClient
	Templates *tpl.Renderer
	Logger   *slog.Logger
}

// PreviewItem is a rendered message preview.
type PreviewItem struct {
	ContactID uint
	Email     string
	FullName  string
	Subject   string
	Body      string
	Step      int
}

// Preview renders messages without sending.
func (s *Service) Preview(ctx context.Context, campaignName string, limit int) ([]PreviewItem, error) {
	camp, err := campaign.GetOrCreate(s.DB, campaignName, "")
	if err != nil {
		return nil, err
	}
	recipients, err := s.loadEligible(camp.ID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]PreviewItem, 0, len(recipients))
	for _, item := range recipients {
		subject, body, err := s.render(0, item.Contact)
		if err != nil {
			return nil, err
		}
		out = append(out, PreviewItem{
			ContactID: item.Contact.ID,
			Email:     item.Contact.Email,
			FullName:  item.Contact.FullName,
			Subject:   subject,
			Body:      body,
			Step:      0,
		})
	}
	return out, nil
}

type recipientBundle struct {
	Recipient database.CampaignRecipient
	Contact   database.Contact
}

func (s *Service) loadEligible(campaignID string, limit int) ([]recipientBundle, error) {
	var contacts []database.Contact
	q := s.DB.Where("approved = ?", true).Order("id asc")
	if err := q.Find(&contacts).Error; err != nil {
		return nil, err
	}
	out := make([]recipientBundle, 0)
	for _, c := range contacts {
		suppressed, _, err := suppression.IsSuppressed(s.DB, c.EmailNormalized)
		if err != nil {
			return nil, err
		}
		if suppressed {
			continue
		}
		rec, err := ensureRecipient(s.DB, campaignID, c.ID)
		if err != nil {
			return nil, err
		}
		if rec.Status == "sent" || rec.Status == "replied" || rec.Status == "unsubscribed" || rec.Status == "suppressed" || rec.Status == "failed" {
			continue
		}
		if rec.StopReason != "" {
			continue
		}
		out = append(out, recipientBundle{Recipient: *rec, Contact: c})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func ensureRecipient(db *gorm.DB, campaignID string, contactID uint) (*database.CampaignRecipient, error) {
	var rec database.CampaignRecipient
	err := db.Where("campaign_id = ? AND contact_id = ?", campaignID, contactID).First(&rec).Error
	if err == nil {
		return &rec, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, err
	}
	rec = database.CampaignRecipient{
		CampaignID: campaignID,
		ContactID:  contactID,
		Status:     "pending",
	}
	if err := db.Create(&rec).Error; err != nil {
		return nil, err
	}
	return &rec, nil
}

// SendOptions configures a send batch.
type SendOptions struct {
	CampaignName string
	Limit        int
	Execute      bool // false => dry-run
	Step         int
}

// SendResult summarizes a batch.
type SendResult struct {
	DryRun   bool
	Sent     int
	Skipped  int
	Failed   int
	Deferred int
	Details  []string
}

// SendInitial sends (or dry-runs) initial outreach emails.
func (s *Service) SendInitial(ctx context.Context, opts SendOptions) (*SendResult, error) {
	if opts.Execute {
		if err := s.Cfg.ValidateForLiveSend(); err != nil {
			return nil, err
		}
		if s.Cfg.DryRun && !opts.Execute {
			// unreachable but kept for clarity
		}
	}
	dryRun := !opts.Execute
	if s.Cfg.DryRun && opts.Execute {
		s.log().Warn("DRY_RUN=true in env; refusing live send unless you set DRY_RUN=false")
		return nil, fmt.Errorf("live send blocked: DRY_RUN=true in configuration; set DRY_RUN=false to enable --execute")
	}

	camp, err := campaign.GetOrCreate(s.DB, opts.CampaignName, "")
	if err != nil {
		return nil, err
	}
	if campaign.IsPaused(camp) {
		return nil, fmt.Errorf("campaign %q is paused", camp.Name)
	}

	recipients, err := s.loadEligible(camp.ID, opts.Limit)
	if err != nil {
		return nil, err
	}

	result := &SendResult{DryRun: dryRun}
	for _, item := range recipients {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !dryRun {
			ok, reason, err := s.canSendNow(ctx, camp)
			if err != nil {
				return result, err
			}
			if !ok {
				result.Deferred++
				result.Details = append(result.Details, fmt.Sprintf("deferred %s: %s", item.Contact.Email, reason))
				break
			}
		}

		outcome, detail, err := s.sendOne(ctx, camp, item, 0, dryRun)
		if err != nil {
			result.Failed++
			result.Details = append(result.Details, detail+": "+err.Error())
			continue
		}
		switch outcome {
		case "sent", "dry_run":
			result.Sent++
		case "skipped":
			result.Skipped++
		default:
			result.Failed++
		}
		result.Details = append(result.Details, detail)

		if !dryRun && s.Cfg.MinSendIntervalSeconds > 0 {
			select {
			case <-ctx.Done():
				return result, ctx.Err()
			case <-time.After(s.Cfg.MinSendInterval()):
			}
		}
	}
	return result, nil
}

func (s *Service) sendOne(ctx context.Context, camp *database.Campaign, item recipientBundle, step int, dryRun bool) (outcome, detail string, err error) {
	suppressed, reason, err := suppression.IsSuppressed(s.DB, item.Contact.EmailNormalized)
	if err != nil {
		return "failed", item.Contact.Email, err
	}
	if suppressed {
		_ = s.DB.Model(&item.Recipient).Updates(map[string]any{"status": "suppressed", "stop_reason": reason}).Error
		return "skipped", fmt.Sprintf("skipped %s (suppressed: %s)", item.Contact.Email, reason), nil
	}

	idem := fmt.Sprintf("%s:%d:%d", camp.ID, item.Contact.ID, step)
	var existing database.SendAttempt
	if err := s.DB.Where("idempotency_key = ?", idem).First(&existing).Error; err == nil {
		if existing.Status == "success" || existing.Status == "dry_run" {
			return "skipped", fmt.Sprintf("skipped %s (already attempted: %s)", item.Contact.Email, existing.Status), nil
		}
		if existing.Status == "ambiguous" {
			reconciled, rerr := s.reconcile(ctx, &existing)
			if rerr != nil {
				return "failed", item.Contact.Email, rerr
			}
			if reconciled {
				return "skipped", fmt.Sprintf("skipped %s (reconciled prior ambiguous send)", item.Contact.Email), nil
			}
		}
	}

	subject, body, err := s.render(step, item.Contact)
	if err != nil {
		return "failed", item.Contact.Email, err
	}

	now := time.Now().UTC()
	attempt := database.SendAttempt{
		IdempotencyKey:      idem,
		CampaignID:          camp.ID,
		ContactID:           item.Contact.ID,
		CampaignRecipientID: item.Recipient.ID,
		Step:                step,
		Status:              "pending",
		AttemptedAt:         now,
	}
	if err := s.DB.Where(database.SendAttempt{IdempotencyKey: idem}).
		Attrs(attempt).FirstOrCreate(&attempt).Error; err != nil {
		return "failed", item.Contact.Email, err
	}

	em := database.EmailMessage{
		CampaignID:          camp.ID,
		ContactID:           item.Contact.ID,
		CampaignRecipientID: item.Recipient.ID,
		Direction:           "outbound",
		Step:                step,
		Subject:             subject,
		BodyPreview:         truncate(body, 500),
		Status:              "pending",
	}

	if dryRun {
		attempt.Status = "dry_run"
		em.Status = "dry_run"
		em.SentAt = &now
		if err := s.DB.Save(&attempt).Error; err != nil {
			return "failed", item.Contact.Email, err
		}
		if err := s.DB.Create(&em).Error; err != nil {
			return "failed", item.Contact.Email, err
		}
		return "dry_run", fmt.Sprintf("dry-run %s — %s", item.Contact.Email, subject), nil
	}

	if s.Mail == nil {
		return "failed", item.Contact.Email, fmt.Errorf("gmail client not configured; run `outreach auth`")
	}

	// Pre-send reply check for follow-ups
	if step > 0 && item.Recipient.ThreadID != "" {
		hasReply, herr := s.Mail.HasReply(ctx, item.Recipient.ThreadID)
		if herr != nil {
			return "failed", item.Contact.Email, fmt.Errorf("reply check failed (postponing): %w", herr)
		}
		if hasReply {
			_ = s.DB.Model(&item.Recipient).Updates(map[string]any{"status": "replied", "stop_reason": "reply_detected"}).Error
			_ = scheduler.CancelFollowups(s.DB, item.Recipient.ID, "reply_detected")
			return "skipped", fmt.Sprintf("skipped %s (reply detected)", item.Contact.Email), nil
		}
	}

	res, sendErr := s.Mail.Send(ctx, gmail.OutboundMessage{
		To:             item.Contact.Email,
		Subject:        subject,
		BodyText:       body,
		ThreadID:       item.Recipient.ThreadID,
		IdempotencyKey: idem,
		InReplyTo:      item.Recipient.LastMessageID,
	})
	if sendErr != nil {
		if isAmbiguous(sendErr) {
			attempt.Status = "ambiguous"
			attempt.Error = sendErr.Error()
			_ = s.DB.Save(&attempt).Error
			em.Status = "ambiguous"
			em.Error = sendErr.Error()
			_ = s.DB.Create(&em).Error
			return "failed", fmt.Sprintf("ambiguous %s", item.Contact.Email), sendErr
		}
		attempt.Status = "failed"
		attempt.Error = sendErr.Error()
		_ = s.DB.Save(&attempt).Error
		em.Status = "failed"
		em.Error = sendErr.Error()
		_ = s.DB.Create(&em).Error
		if isPermanent(sendErr) {
			_ = s.DB.Model(&item.Recipient).Updates(map[string]any{"status": "failed", "stop_reason": "permanent_failure"}).Error
			_ = scheduler.CancelFollowups(s.DB, item.Recipient.ID, "permanent_failure")
		}
		return "failed", fmt.Sprintf("failed %s", item.Contact.Email), sendErr
	}

	attempt.Status = "success"
	attempt.GmailMessageID = res.MessageID
	attempt.GmailThreadID = res.ThreadID
	_ = s.DB.Save(&attempt).Error

	em.Status = "sent"
	em.GmailMessageID = res.MessageID
	em.GmailThreadID = res.ThreadID
	em.SentAt = &now
	_ = s.DB.Create(&em).Error

	updates := map[string]any{
		"last_message_id": res.MessageID,
		"thread_id":       res.ThreadID,
		"status":          "sent",
	}
	if step == 0 {
		updates["initial_sent_at"] = now
	}
	_ = s.DB.Model(&item.Recipient).Updates(updates).Error

	if step == 0 && s.Cfg.FollowupEnabled {
		_ = scheduler.ScheduleFollowups(s.DB, s.Cfg, camp.ID, item.Contact.ID, item.Recipient.ID, now)
	}

	_ = database.SetSetting(s.DB, "last_send_at", now.Format(time.RFC3339))
	return "sent", fmt.Sprintf("sent %s msg=%s", item.Contact.Email, res.MessageID), nil
}

func (s *Service) reconcile(ctx context.Context, attempt *database.SendAttempt) (bool, error) {
	if s.Mail == nil {
		return false, fmt.Errorf("cannot reconcile without gmail client")
	}
	found, err := s.Mail.FindMessageByIdempotency(ctx, attempt.IdempotencyKey)
	if err != nil {
		return false, err
	}
	if found == nil {
		// Safe to retry: mark failed so a new attempt can proceed with same key after status change.
		attempt.Status = "failed"
		attempt.Error = "reconcile: message not found; safe to retry"
		return false, s.DB.Save(attempt).Error
	}
	attempt.Status = "success"
	attempt.GmailMessageID = found.MessageID
	attempt.GmailThreadID = found.ThreadID
	if err := s.DB.Save(attempt).Error; err != nil {
		return false, err
	}
	_ = s.DB.Model(&database.CampaignRecipient{}).Where("id = ?", attempt.CampaignRecipientID).
		Updates(map[string]any{
			"status":          "sent",
			"last_message_id": found.MessageID,
			"thread_id":       found.ThreadID,
		}).Error
	return true, nil
}

func (s *Service) canSendNow(ctx context.Context, camp *database.Campaign) (bool, string, error) {
	if campaign.IsPaused(camp) {
		return false, "campaign paused", nil
	}
	loc, err := time.LoadLocation(s.Cfg.SendTimezone)
	if err != nil {
		return false, "", err
	}
	now := time.Now().In(loc)
	hour := now.Hour()
	start, end := s.Cfg.SendStartHour, s.Cfg.SendEndHour
	inWindow := false
	if start == end {
		inWindow = true
	} else if start < end {
		inWindow = hour >= start && hour < end
	} else {
		inWindow = hour >= start || hour < end
	}
	if !inWindow {
		return false, fmt.Sprintf("outside business hours (%d-%d %s)", start, end, s.Cfg.SendTimezone), nil
	}

	sinceHour := now.UTC().Truncate(time.Hour)
	sinceDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).UTC()

	var hourly, daily int64
	if err := s.DB.Model(&database.SendAttempt{}).
		Where("status = ? AND attempted_at >= ?", "success", sinceHour).
		Count(&hourly).Error; err != nil {
		return false, "", err
	}
	if err := s.DB.Model(&database.SendAttempt{}).
		Where("status = ? AND attempted_at >= ?", "success", sinceDay).
		Count(&daily).Error; err != nil {
		return false, "", err
	}
	if int(hourly) >= s.Cfg.HourlySendLimit {
		return false, "hourly send limit reached", nil
	}
	if int(daily) >= s.Cfg.DailySendLimit {
		return false, "daily send limit reached", nil
	}

	if last, ok, err := database.GetSetting(s.DB, "last_send_at"); err == nil && ok {
		if t, perr := time.Parse(time.RFC3339, last); perr == nil {
			if time.Since(t) < s.Cfg.MinSendInterval() {
				return false, "min send interval not elapsed", nil
			}
		}
	}
	_ = ctx
	return true, "", nil
}

func (s *Service) render(step int, c database.Contact) (string, string, error) {
	name := tpl.TemplateForStep(step)
	return s.Templates.Render(name, tpl.Data{
		FirstName:       c.FirstName,
		FullName:        c.FullName,
		Email:           c.Email,
		Country:         c.Country,
		Specialization:  c.Specialization,
		ProfileLinks:    c.ProfileLinks,
		SenderName:      s.Cfg.SenderName,
		SenderEmail:     s.Cfg.SenderEmail,
		BusinessName:    s.Cfg.BusinessName,
		BusinessAddress: s.Cfg.BusinessAddress,
		OptOutEmail:     s.Cfg.OptOutEmail,
	})
}

func (s *Service) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func isAmbiguous(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "ambiguous") || strings.Contains(msg, "connection reset")
}

func isPermanent(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "invalid recipient") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "blocked") ||
		strings.Contains(msg, "permanent")
}

// SendFollowupJob processes a single claimed follow-up job.
func (s *Service) SendFollowupJob(ctx context.Context, job *database.FollowupJob, dryRun bool) error {
	camp, err := campaign.GetByIDOrName(s.DB, job.CampaignID)
	if err != nil {
		return err
	}
	if campaign.IsPaused(camp) {
		return scheduler.PostponeJob(s.DB, job, "campaign paused")
	}
	var rec database.CampaignRecipient
	if err := s.DB.First(&rec, job.CampaignRecipientID).Error; err != nil {
		return err
	}
	var contact database.Contact
	if err := s.DB.First(&contact, job.ContactID).Error; err != nil {
		return err
	}
	ok, reason, err := s.canSendNow(ctx, camp)
	if err != nil {
		return err
	}
	if !ok {
		return scheduler.PostponeJob(s.DB, job, reason)
	}
	_, _, err = s.sendOne(ctx, camp, recipientBundle{Recipient: rec, Contact: contact}, job.Step, dryRun)
	if err != nil {
		if strings.Contains(err.Error(), "reply check failed") {
			return scheduler.PostponeJob(s.DB, job, err.Error())
		}
		return err
	}
	return scheduler.CompleteJob(s.DB, job)
}
