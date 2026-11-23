-- Reference schema for FWD email outreach (SQLite).
-- Applied automatically via GORM AutoMigrate; this file documents the intended shape.

CREATE TABLE IF NOT EXISTS contacts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  full_name TEXT NOT NULL,
  first_name TEXT NOT NULL,
  email TEXT NOT NULL UNIQUE,
  email_normalized TEXT NOT NULL UNIQUE,
  country TEXT,
  specialization TEXT,
  profile_links TEXT,
  original_row_json TEXT,
  source_file TEXT,
  import_timestamp DATETIME NOT NULL,
  approved INTEGER NOT NULL DEFAULT 0,
  approved_at DATETIME,
  permission_note TEXT,
  created_at DATETIME,
  updated_at DATETIME
);

CREATE TABLE IF NOT EXISTS campaigns (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  status TEXT NOT NULL DEFAULT 'active',
  subject TEXT,
  description TEXT,
  created_at DATETIME,
  updated_at DATETIME
);

CREATE TABLE IF NOT EXISTS campaign_recipients (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  campaign_id TEXT NOT NULL,
  contact_id INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  initial_sent_at DATETIME,
  last_message_id TEXT,
  thread_id TEXT,
  stop_reason TEXT,
  created_at DATETIME,
  updated_at DATETIME,
  UNIQUE(campaign_id, contact_id)
);

CREATE TABLE IF NOT EXISTS email_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  campaign_id TEXT NOT NULL,
  contact_id INTEGER NOT NULL,
  campaign_recipient_id INTEGER,
  direction TEXT NOT NULL,
  step INTEGER NOT NULL DEFAULT 0,
  subject TEXT,
  body_preview TEXT,
  gmail_message_id TEXT,
  gmail_thread_id TEXT,
  status TEXT NOT NULL,
  error TEXT,
  sent_at DATETIME,
  created_at DATETIME,
  updated_at DATETIME
);

CREATE TABLE IF NOT EXISTS send_attempts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  idempotency_key TEXT NOT NULL UNIQUE,
  campaign_id TEXT NOT NULL,
  contact_id INTEGER NOT NULL,
  campaign_recipient_id INTEGER,
  step INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL,
  gmail_message_id TEXT,
  gmail_thread_id TEXT,
  error TEXT,
  attempted_at DATETIME NOT NULL,
  created_at DATETIME,
  updated_at DATETIME
);

CREATE TABLE IF NOT EXISTS followup_jobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  campaign_id TEXT NOT NULL,
  contact_id INTEGER NOT NULL,
  campaign_recipient_id INTEGER NOT NULL,
  step INTEGER NOT NULL,
  scheduled_at DATETIME NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  claimed_at DATETIME,
  claim_token TEXT,
  attempts INTEGER NOT NULL DEFAULT 0,
  last_error TEXT,
  created_at DATETIME,
  updated_at DATETIME
);

CREATE TABLE IF NOT EXISTS suppression_list (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  email TEXT NOT NULL UNIQUE,
  reason TEXT NOT NULL,
  source TEXT,
  created_at DATETIME,
  updated_at DATETIME
);

CREATE TABLE IF NOT EXISTS oauth_account_metadata (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  email TEXT NOT NULL UNIQUE,
  display_name TEXT,
  verified INTEGER NOT NULL DEFAULT 0,
  token_path TEXT,
  last_refreshed DATETIME,
  created_at DATETIME,
  updated_at DATETIME
);

CREATE TABLE IF NOT EXISTS app_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at DATETIME
);
