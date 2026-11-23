package suppression

import (
	"fmt"
	"strings"
	"time"

	"github.com/smartblack24/email-script/fwd-email-outreach/internal/database"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/importer"
	"gorm.io/gorm"
)

// Add permanently suppresses an email address. Opted-out contacts are never auto-reactivated.
func Add(db *gorm.DB, email, reason, source string) error {
	norm := importer.NormalizeEmail(email)
	if !importer.ValidEmail(email) {
		return fmt.Errorf("invalid email: %s", email)
	}
	if strings.TrimSpace(reason) == "" {
		reason = "manual_suppression"
	}
	entry := database.SuppressionEntry{
		Email:     norm,
		Reason:    reason,
		Source:    source,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	return db.Where(database.SuppressionEntry{Email: norm}).
		Assign(database.SuppressionEntry{Reason: reason, Source: source, UpdatedAt: time.Now().UTC()}).
		FirstOrCreate(&entry).Error
}

// IsSuppressed reports whether an email is on the suppression list.
func IsSuppressed(db *gorm.DB, email string) (bool, string, error) {
	norm := importer.NormalizeEmail(email)
	var entry database.SuppressionEntry
	err := db.Where("email = ?", norm).First(&entry).Error
	if err == gorm.ErrRecordNotFound {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	return true, entry.Reason, nil
}

// List returns all suppression entries.
func List(db *gorm.DB) ([]database.SuppressionEntry, error) {
	var entries []database.SuppressionEntry
	err := db.Order("created_at desc").Find(&entries).Error
	return entries, err
}
