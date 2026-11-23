package tracker

import (
	"context"
	"fmt"
	"time"

	"github.com/smartblack24/email-script/fwd-email-outreach/internal/database"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/gmail"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/scheduler"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/suppression"
	"github.com/xuri/excelize/v2"
	"gorm.io/gorm"
)

// StatusReport is a high-level campaign/system status snapshot.
type StatusReport struct {
	ContactsTotal     int64
	ContactsApproved  int64
	Suppressed        int64
	Campaigns         []CampaignStatus
	MessagesSent      int64
	MessagesDryRun    int64
	FollowupsPending  int64
	FollowupsClaimed  int64
	FollowupsDone     int64
	LastSendAt        string
}

// CampaignStatus summarizes one campaign.
type CampaignStatus struct {
	ID       string
	Name     string
	Status   string
	Pending  int64
	Sent     int64
	Replied  int64
	Failed   int64
	Stopped  int64
}

// Collect builds a status report.
func Collect(db *gorm.DB) (*StatusReport, error) {
	r := &StatusReport{}
	_ = db.Model(&database.Contact{}).Count(&r.ContactsTotal).Error
	_ = db.Model(&database.Contact{}).Where("approved = ?", true).Count(&r.ContactsApproved).Error
	_ = db.Model(&database.SuppressionEntry{}).Count(&r.Suppressed).Error
	_ = db.Model(&database.EmailMessage{}).Where("status = ?", "sent").Count(&r.MessagesSent).Error
	_ = db.Model(&database.EmailMessage{}).Where("status = ?", "dry_run").Count(&r.MessagesDryRun).Error
	_ = db.Model(&database.FollowupJob{}).Where("status = ?", "pending").Count(&r.FollowupsPending).Error
	_ = db.Model(&database.FollowupJob{}).Where("status = ?", "claimed").Count(&r.FollowupsClaimed).Error
	_ = db.Model(&database.FollowupJob{}).Where("status = ?", "done").Count(&r.FollowupsDone).Error
	if v, ok, _ := database.GetSetting(db, "last_send_at"); ok {
		r.LastSendAt = v
	}

	var camps []database.Campaign
	if err := db.Find(&camps).Error; err != nil {
		return nil, err
	}
	for _, c := range camps {
		cs := CampaignStatus{ID: c.ID, Name: c.Name, Status: c.Status}
		_ = db.Model(&database.CampaignRecipient{}).Where("campaign_id = ? AND status = ?", c.ID, "pending").Count(&cs.Pending).Error
		_ = db.Model(&database.CampaignRecipient{}).Where("campaign_id = ? AND status = ?", c.ID, "sent").Count(&cs.Sent).Error
		_ = db.Model(&database.CampaignRecipient{}).Where("campaign_id = ? AND status = ?", c.ID, "replied").Count(&cs.Replied).Error
		_ = db.Model(&database.CampaignRecipient{}).Where("campaign_id = ? AND status = ?", c.ID, "failed").Count(&cs.Failed).Error
		_ = db.Model(&database.CampaignRecipient{}).Where("campaign_id = ? AND status IN ?", c.ID, []string{"unsubscribed", "suppressed"}).Count(&cs.Stopped).Error
		r.Campaigns = append(r.Campaigns, cs)
	}
	return r, nil
}

// CheckReplies scans sent recipients for inbound replies and stops follow-ups.
func CheckReplies(db *gorm.DB, mail gmail.MailClient) (int, error) {
	if mail == nil {
		return 0, fmt.Errorf("gmail client required")
	}
	var recipients []database.CampaignRecipient
	if err := db.Where("status = ? AND thread_id <> ''", "sent").Find(&recipients).Error; err != nil {
		return 0, err
	}
	found := 0
	for _, rec := range recipients {
		has, err := mail.HasReply(context.Background(), rec.ThreadID)
		if err != nil {
			return found, err
		}
		if !has {
			continue
		}
		_ = db.Model(&rec).Updates(map[string]any{
			"status":      "replied",
			"stop_reason": "reply_detected",
			"updated_at":  time.Now().UTC(),
		}).Error
		_ = scheduler.CancelFollowups(db, rec.ID, "reply_detected")
		_ = db.Create(&database.EmailMessage{
			CampaignID:          rec.CampaignID,
			ContactID:           rec.ContactID,
			CampaignRecipientID: rec.ID,
			Direction:           "inbound",
			GmailThreadID:       rec.ThreadID,
			Status:              "replied",
		}).Error
		found++
	}
	return found, nil
}

// ExportExcel writes a report workbook.
func ExportExcel(db *gorm.DB, path string) error {
	f := excelize.NewFile()
	defer f.Close()

	contactsSheet := "Contacts"
	_ = f.SetSheetName("Sheet1", contactsSheet)
	contactHeaders := []string{"ID", "Full Name", "First Name", "Email", "Country", "Specialization", "Approved", "Source File", "Import Timestamp"}
	for i, h := range contactHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = f.SetCellValue(contactsSheet, cell, h)
	}
	var contacts []database.Contact
	if err := db.Order("id asc").Find(&contacts).Error; err != nil {
		return err
	}
	for i, c := range contacts {
		row := i + 2
		vals := []any{c.ID, c.FullName, c.FirstName, c.Email, c.Country, c.Specialization, c.Approved, c.SourceFile, c.ImportTimestamp.UTC().Format(time.RFC3339)}
		for col, v := range vals {
			cell, _ := excelize.CoordinatesToCellName(col+1, row)
			_ = f.SetCellValue(contactsSheet, cell, v)
		}
	}

	msgSheet := "Messages"
	_, _ = f.NewSheet(msgSheet)
	msgHeaders := []string{"ID", "Campaign ID", "Contact ID", "Step", "Subject", "Status", "Gmail Message ID", "Thread ID", "Sent At"}
	for i, h := range msgHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = f.SetCellValue(msgSheet, cell, h)
	}
	var msgs []database.EmailMessage
	if err := db.Order("id asc").Find(&msgs).Error; err != nil {
		return err
	}
	for i, m := range msgs {
		row := i + 2
		sent := ""
		if m.SentAt != nil {
			sent = m.SentAt.UTC().Format(time.RFC3339)
		}
		vals := []any{m.ID, m.CampaignID, m.ContactID, m.Step, m.Subject, m.Status, m.GmailMessageID, m.GmailThreadID, sent}
		for col, v := range vals {
			cell, _ := excelize.CoordinatesToCellName(col+1, row)
			_ = f.SetCellValue(msgSheet, cell, v)
		}
	}

	suppSheet := "Suppression"
	_, _ = f.NewSheet(suppSheet)
	_ = f.SetCellValue(suppSheet, "A1", "Email")
	_ = f.SetCellValue(suppSheet, "B1", "Reason")
	_ = f.SetCellValue(suppSheet, "C1", "Source")
	_ = f.SetCellValue(suppSheet, "D1", "Created At")
	entries, err := suppression.List(db)
	if err != nil {
		return err
	}
	for i, e := range entries {
		row := i + 2
		_ = f.SetCellValue(suppSheet, fmt.Sprintf("A%d", row), e.Email)
		_ = f.SetCellValue(suppSheet, fmt.Sprintf("B%d", row), e.Reason)
		_ = f.SetCellValue(suppSheet, fmt.Sprintf("C%d", row), e.Source)
		_ = f.SetCellValue(suppSheet, fmt.Sprintf("D%d", row), e.CreatedAt.UTC().Format(time.RFC3339))
	}

	jobsSheet := "Followups"
	_, _ = f.NewSheet(jobsSheet)
	jobHeaders := []string{"ID", "Campaign ID", "Contact ID", "Step", "Scheduled At", "Status", "Attempts", "Last Error"}
	for i, h := range jobHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = f.SetCellValue(jobsSheet, cell, h)
	}
	var jobs []database.FollowupJob
	if err := db.Order("id asc").Find(&jobs).Error; err != nil {
		return err
	}
	for i, j := range jobs {
		row := i + 2
		vals := []any{j.ID, j.CampaignID, j.ContactID, j.Step, j.ScheduledAt.UTC().Format(time.RFC3339), j.Status, j.Attempts, j.LastError}
		for col, v := range vals {
			cell, _ := excelize.CoordinatesToCellName(col+1, row)
			_ = f.SetCellValue(jobsSheet, cell, v)
		}
	}

	return f.SaveAs(path)
}
