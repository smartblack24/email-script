package database

import "time"

// Contact is an imported outreach prospect.
type Contact struct {
	ID              uint      `gorm:"primaryKey"`
	FullName        string    `gorm:"not null"`
	FirstName       string    `gorm:"not null"`
	Email           string    `gorm:"not null;uniqueIndex"`
	EmailNormalized string    `gorm:"not null;uniqueIndex"`
	Country         string
	Specialization  string
	ProfileLinks    string
	OriginalRowJSON string
	SourceFile      string
	ImportTimestamp time.Time `gorm:"not null"`
	Approved        bool      `gorm:"not null;default:false"`
	ApprovedAt      *time.Time
	PermissionNote  string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Campaign groups recipients and send settings.
type Campaign struct {
	ID          string `gorm:"primaryKey;size:64"`
	Name        string `gorm:"uniqueIndex;not null"`
	Status      string `gorm:"not null;default:active"` // active | paused
	Subject     string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// CampaignRecipient links a contact to a campaign with send state.
type CampaignRecipient struct {
	ID              uint   `gorm:"primaryKey"`
	CampaignID      string `gorm:"not null;uniqueIndex:idx_campaign_contact"`
	ContactID       uint   `gorm:"not null;uniqueIndex:idx_campaign_contact"`
	Status          string `gorm:"not null;default:pending"` // pending|queued|sent|replied|suppressed|failed|unsubscribed
	InitialSentAt   *time.Time
	LastMessageID   string
	ThreadID        string
	StopReason      string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// EmailMessage stores outbound (and detected inbound) message metadata.
type EmailMessage struct {
	ID                 uint   `gorm:"primaryKey"`
	CampaignID         string `gorm:"index;not null"`
	ContactID          uint   `gorm:"index;not null"`
	CampaignRecipientID uint  `gorm:"index"`
	Direction          string `gorm:"not null"` // outbound|inbound
	Step               int    `gorm:"not null;default:0"`
	Subject            string
	BodyPreview        string
	GmailMessageID     string `gorm:"index"`
	GmailThreadID      string `gorm:"index"`
	Status             string `gorm:"not null"` // dry_run|sent|failed|ambiguous|reconciled
	Error              string
	SentAt             *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// SendAttempt records each send try for idempotency and reconciliation.
type SendAttempt struct {
	ID                 uint   `gorm:"primaryKey"`
	IdempotencyKey     string `gorm:"uniqueIndex;not null"`
	CampaignID         string `gorm:"index;not null"`
	ContactID          uint   `gorm:"index;not null"`
	CampaignRecipientID uint  `gorm:"index"`
	Step               int    `gorm:"not null;default:0"`
	Status             string `gorm:"not null"` // pending|success|failed|ambiguous|dry_run
	GmailMessageID     string
	GmailThreadID      string
	Error              string
	AttemptedAt        time.Time `gorm:"not null"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// FollowupJob is a scheduled follow-up send.
type FollowupJob struct {
	ID                 uint       `gorm:"primaryKey"`
	CampaignID         string     `gorm:"index;not null"`
	ContactID          uint       `gorm:"index;not null"`
	CampaignRecipientID uint      `gorm:"index;not null"`
	Step               int        `gorm:"not null"`
	ScheduledAt        time.Time  `gorm:"index;not null"`
	Status             string     `gorm:"not null;default:pending"` // pending|claimed|done|cancelled|postponed
	ClaimedAt          *time.Time
	ClaimToken         string     `gorm:"index"`
	Attempts           int        `gorm:"not null;default:0"`
	LastError          string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// SuppressionEntry permanently blocks an email address.
type SuppressionEntry struct {
	ID        uint      `gorm:"primaryKey"`
	Email     string    `gorm:"uniqueIndex;not null"`
	Reason    string    `gorm:"not null"`
	Source    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// OAuthAccountMetadata stores Gmail account verification details.
type OAuthAccountMetadata struct {
	ID           uint   `gorm:"primaryKey"`
	Email        string `gorm:"uniqueIndex;not null"`
	DisplayName  string
	Verified     bool   `gorm:"not null;default:false"`
	TokenPath    string
	LastRefreshed *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// AppSetting stores simple key/value runtime flags.
type AppSetting struct {
	Key       string `gorm:"primaryKey"`
	Value     string `gorm:"not null"`
	UpdatedAt time.Time
}
