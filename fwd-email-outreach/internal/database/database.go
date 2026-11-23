package database

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Open opens (or creates) the SQLite database and runs AutoMigrate.
func Open(path string) (*gorm.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
		NowFunc: func() time.Time {
			return time.Now().UTC()
		},
	})
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)

	if err := Migrate(db); err != nil {
		return nil, err
	}
	return db, nil
}

// Migrate applies schema migrations via GORM AutoMigrate.
func Migrate(db *gorm.DB) error {
	if err := db.AutoMigrate(
		&Contact{},
		&Campaign{},
		&CampaignRecipient{},
		&EmailMessage{},
		&SendAttempt{},
		&FollowupJob{},
		&SuppressionEntry{},
		&OAuthAccountMetadata{},
		&AppSetting{},
	); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// GetSetting reads a key/value setting.
func GetSetting(db *gorm.DB, key string) (string, bool, error) {
	var s AppSetting
	err := db.First(&s, "key = ?", key).Error
	if err == gorm.ErrRecordNotFound {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return s.Value, true, nil
}

// SetSetting upserts a key/value setting.
func SetSetting(db *gorm.DB, key, value string) error {
	s := AppSetting{Key: key, Value: value, UpdatedAt: time.Now().UTC()}
	return db.Save(&s).Error
}
