package contacts

import (
	"encoding/csv"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/smartblack24/email-script/fwd-email-outreach/internal/database"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/importer"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/suppression"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PersistImport writes valid contacts into SQLite (deduped by normalized email).
func PersistImport(db *gorm.DB, summary *importer.Summary, permissionNote string) (inserted, updated int, err error) {
	now := time.Now().UTC()
	err = db.Transaction(func(tx *gorm.DB) error {
		for _, rc := range summary.Contacts {
			if !rc.Valid {
				continue
			}
			suppressed, _, serr := suppression.IsSuppressed(tx, rc.EmailNormalized)
			if serr != nil {
				return serr
			}
			c := database.Contact{
				FullName:        rc.FullName,
				FirstName:       rc.FirstName,
				Email:           rc.Email,
				EmailNormalized: rc.EmailNormalized,
				Country:         rc.Country,
				Specialization:  rc.Specialization,
				ProfileLinks:    rc.ProfileLinks,
				OriginalRowJSON: importer.OriginalJSON(rc.OriginalRow),
				SourceFile:      summary.SourceFile,
				ImportTimestamp: now,
				Approved:        false,
				PermissionNote:  permissionNote,
			}
			if suppressed {
				// Still store for audit, but leave unapproved; never email.
				c.PermissionNote = strings.TrimSpace(permissionNote + " [suppressed]")
			}

			var existing database.Contact
			findErr := tx.Where("email_normalized = ?", rc.EmailNormalized).First(&existing).Error
			if findErr == gorm.ErrRecordNotFound {
				if err := tx.Create(&c).Error; err != nil {
					return err
				}
				inserted++
				continue
			}
			if findErr != nil {
				return findErr
			}
			// Update profile fields but never auto-approve or clear opt-out history.
			existing.FullName = c.FullName
			existing.FirstName = c.FirstName
			existing.Email = c.Email
			existing.Country = c.Country
			existing.Specialization = c.Specialization
			existing.ProfileLinks = c.ProfileLinks
			existing.OriginalRowJSON = c.OriginalRowJSON
			existing.SourceFile = c.SourceFile
			existing.ImportTimestamp = now
			if permissionNote != "" {
				existing.PermissionNote = permissionNote
			}
			if err := tx.Save(&existing).Error; err != nil {
				return err
			}
			updated++
		}
		return nil
	})
	return inserted, updated, err
}

// ListFilter filters contact listing.
type ListFilter struct {
	Approved *bool
	Limit    int
	Offset   int
	Query    string
}

// List returns contacts matching the filter.
func List(db *gorm.DB, f ListFilter) ([]database.Contact, error) {
	q := db.Model(&database.Contact{})
	if f.Approved != nil {
		q = q.Where("approved = ?", *f.Approved)
	}
	if f.Query != "" {
		like := "%" + f.Query + "%"
		q = q.Where("full_name LIKE ? OR email LIKE ? OR country LIKE ?", like, like, like)
	}
	if f.Limit > 0 {
		q = q.Limit(f.Limit)
	}
	if f.Offset > 0 {
		q = q.Offset(f.Offset)
	}
	var out []database.Contact
	err := q.Order("id asc").Find(&out).Error
	return out, err
}

// ApproveFromCSV marks contacts approved when their email appears in the CSV.
// CSV must contain an Email column (header flexible).
func ApproveFromCSV(db *gorm.DB, path string) (approved int, missing int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return 0, 0, err
	}
	if len(rows) == 0 {
		return 0, 0, fmt.Errorf("empty approval file")
	}

	emailIdx := -1
	for i, h := range rows[0] {
		if importer.NormalizeHeader(h) == "email" || strings.Contains(importer.NormalizeHeader(h), "email") {
			emailIdx = i
			break
		}
	}
	if emailIdx < 0 {
		return 0, 0, fmt.Errorf("approval CSV must include an Email column")
	}

	now := time.Now().UTC()
	err = db.Transaction(func(tx *gorm.DB) error {
		for i := 1; i < len(rows); i++ {
			row := rows[i]
			if emailIdx >= len(row) {
				missing++
				continue
			}
			email := importer.NormalizeEmail(row[emailIdx])
			if !importer.ValidEmail(email) {
				missing++
				continue
			}
			suppressed, _, serr := suppression.IsSuppressed(tx, email)
			if serr != nil {
				return serr
			}
			if suppressed {
				// Never approve suppressed addresses for sending.
				missing++
				continue
			}
			res := tx.Model(&database.Contact{}).
				Where("email_normalized = ?", email).
				Updates(map[string]any{
					"approved":    true,
					"approved_at": now,
				})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				missing++
			} else {
				approved += int(res.RowsAffected)
			}
		}
		return nil
	})
	return approved, missing, err
}

// GetApprovedForCampaign returns approved, non-suppressed contacts not yet attached or attachable.
func GetApprovedSendable(db *gorm.DB, limit int) ([]database.Contact, error) {
	var contacts []database.Contact
	q := db.Where("approved = ?", true).Order("id asc")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&contacts).Error; err != nil {
		return nil, err
	}
	out := make([]database.Contact, 0, len(contacts))
	for _, c := range contacts {
		suppressed, _, err := suppression.IsSuppressed(db, c.EmailNormalized)
		if err != nil {
			return nil, err
		}
		if suppressed {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// EnsureRecipient upserts a campaign recipient row.
func EnsureRecipient(db *gorm.DB, campaignID string, contactID uint) (*database.CampaignRecipient, error) {
	rec := database.CampaignRecipient{
		CampaignID: campaignID,
		ContactID:  contactID,
		Status:     "pending",
	}
	err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "campaign_id"}, {Name: "contact_id"}},
		DoNothing: true,
	}).Create(&rec).Error
	if err != nil {
		return nil, err
	}
	var out database.CampaignRecipient
	if err := db.Where("campaign_id = ? AND contact_id = ?", campaignID, contactID).First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}
