package campaign

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/smartblack24/email-script/fwd-email-outreach/internal/database"
	"gorm.io/gorm"
)

const (
	StatusActive = "active"
	StatusPaused = "paused"
)

// GetOrCreate returns an existing campaign by name or creates one with a unique ID.
func GetOrCreate(db *gorm.DB, name, subject string) (*database.Campaign, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "default"
	}
	var c database.Campaign
	err := db.Where("name = ?", name).First(&c).Error
	if err == nil {
		return &c, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, err
	}
	c = database.Campaign{
		ID:        uuid.NewString(),
		Name:      name,
		Status:    StatusActive,
		Subject:   subject,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := db.Create(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// GetByIDOrName finds a campaign.
func GetByIDOrName(db *gorm.DB, idOrName string) (*database.Campaign, error) {
	var c database.Campaign
	err := db.Where("id = ? OR name = ?", idOrName, idOrName).First(&c).Error
	if err != nil {
		return nil, fmt.Errorf("campaign %q not found: %w", idOrName, err)
	}
	return &c, nil
}

// Pause sets campaign status to paused.
func Pause(db *gorm.DB, idOrName string) (*database.Campaign, error) {
	c, err := GetByIDOrName(db, idOrName)
	if err != nil {
		return nil, err
	}
	c.Status = StatusPaused
	c.UpdatedAt = time.Now().UTC()
	if err := db.Save(c).Error; err != nil {
		return nil, err
	}
	return c, nil
}

// Resume sets campaign status to active.
func Resume(db *gorm.DB, idOrName string) (*database.Campaign, error) {
	c, err := GetByIDOrName(db, idOrName)
	if err != nil {
		return nil, err
	}
	c.Status = StatusActive
	c.UpdatedAt = time.Now().UTC()
	if err := db.Save(c).Error; err != nil {
		return nil, err
	}
	return c, nil
}

// IsPaused reports whether a campaign is paused.
func IsPaused(c *database.Campaign) bool {
	return c != nil && c.Status == StatusPaused
}

// List returns all campaigns.
func List(db *gorm.DB) ([]database.Campaign, error) {
	var out []database.Campaign
	err := db.Order("created_at asc").Find(&out).Error
	return out, err
}
