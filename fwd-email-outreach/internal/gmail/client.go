package gmail

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"gorm.io/gorm"

	"github.com/smartblack24/email-script/fwd-email-outreach/internal/database"
)

// MailClient abstracts Gmail operations for production and tests.
type MailClient interface {
	SenderEmail() string
	Send(ctx context.Context, msg OutboundMessage) (*SendResult, error)
	HasReply(ctx context.Context, threadID string) (bool, error)
	FindMessageByIdempotency(ctx context.Context, key string) (*SendResult, error)
}

// OutboundMessage is a message to send.
type OutboundMessage struct {
	To             string
	Subject        string
	BodyText       string
	BodyHTML       string
	ThreadID       string
	IdempotencyKey string
	InReplyTo      string
}

// SendResult captures Gmail IDs after a send.
type SendResult struct {
	MessageID string
	ThreadID  string
}

// Client is the real Gmail API client with OAuth token refresh.
type Client struct {
	svc    *gmail.Service
	email  string
	logger *slog.Logger
}

// AuthConfig holds OAuth file paths.
type AuthConfig struct {
	CredentialsFile string
	TokenFile       string
}

// Authenticate runs the OAuth installed-app flow (or refreshes an existing token).
func Authenticate(ctx context.Context, cfg AuthConfig, db *gorm.DB, logger *slog.Logger) (*Client, error) {
	if logger == nil {
		logger = slog.Default()
	}
	b, err := os.ReadFile(cfg.CredentialsFile)
	if err != nil {
		return nil, fmt.Errorf("read credentials file %s: %w (download OAuth client JSON from Google Cloud Console)", cfg.CredentialsFile, err)
	}
	oauthCfg, err := google.ConfigFromJSON(b, gmail.GmailSendScope, gmail.GmailReadonlyScope, gmail.GmailModifyScope)
	if err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}

	tok, err := tokenFromFile(cfg.TokenFile)
	if err != nil {
		tok, err = authCodeFlow(ctx, oauthCfg)
		if err != nil {
			return nil, err
		}
		if err := saveToken(cfg.TokenFile, tok); err != nil {
			return nil, err
		}
	}

	httpClient := oauthCfg.Client(ctx, tok)
	svc, err := gmail.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("create gmail service: %w", err)
	}

	profile, err := svc.Users.GetProfile("me").Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("verify sender profile: %w", err)
	}

	now := time.Now().UTC()
	meta := database.OAuthAccountMetadata{
		Email:         profile.EmailAddress,
		DisplayName:   profile.EmailAddress,
		Verified:      true,
		TokenPath:     cfg.TokenFile,
		LastRefreshed: &now,
	}
	if db != nil {
		_ = db.Where(database.OAuthAccountMetadata{Email: profile.EmailAddress}).
			Assign(meta).FirstOrCreate(&meta).Error
	}

	logger.Info("gmail authenticated", "email", profile.EmailAddress)
	return &Client{svc: svc, email: profile.EmailAddress, logger: logger}, nil
}

// NewClientFromService constructs a Client around an existing service (tests/advanced).
func NewClientFromService(svc *gmail.Service, email string, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{svc: svc, email: email, logger: logger}
}

func (c *Client) SenderEmail() string { return c.email }

func (c *Client) Send(ctx context.Context, msg OutboundMessage) (*SendResult, error) {
	raw := buildMIME(c.email, msg)
	req := &gmail.Message{
		Raw:      raw,
		ThreadId: msg.ThreadID,
	}
	var sent *gmail.Message
	err := withBackoff(ctx, c.logger, func() error {
		var callErr error
		sent, callErr = c.svc.Users.Messages.Send("me", req).Context(ctx).Do()
		return callErr
	})
	if err != nil {
		return nil, err
	}
	return &SendResult{MessageID: sent.Id, ThreadID: sent.ThreadId}, nil
}

func (c *Client) HasReply(ctx context.Context, threadID string) (bool, error) {
	if threadID == "" {
		return false, nil
	}
	var thread *gmail.Thread
	err := withBackoff(ctx, c.logger, func() error {
		var callErr error
		thread, callErr = c.svc.Users.Threads.Get("me", threadID).Context(ctx).Do()
		return callErr
	})
	if err != nil {
		return false, err
	}
	for _, m := range thread.Messages {
		if m.Payload == nil {
			continue
		}
		from := headerValue(m.Payload.Headers, "From")
		if from == "" {
			continue
		}
		if !strings.Contains(strings.ToLower(from), strings.ToLower(c.email)) {
			return true, nil
		}
	}
	return false, nil
}

func (c *Client) FindMessageByIdempotency(ctx context.Context, key string) (*SendResult, error) {
	if key == "" {
		return nil, nil
	}
	q := fmt.Sprintf(`rfc822msgid:%s OR subject:"%s"`, key, key)
	var list *gmail.ListMessagesResponse
	err := withBackoff(ctx, c.logger, func() error {
		var callErr error
		list, callErr = c.svc.Users.Messages.List("me").Q(q).MaxResults(5).Context(ctx).Do()
		return callErr
	})
	if err != nil {
		return nil, err
	}
	if list == nil || len(list.Messages) == 0 {
		// Also search X-Outreach-Idempotency header via broader query is limited; return nil.
		return nil, nil
	}
	m := list.Messages[0]
	return &SendResult{MessageID: m.Id, ThreadID: m.ThreadId}, nil
}

func headerValue(headers []*gmail.MessagePartHeader, name string) string {
	for _, h := range headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

func withBackoff(ctx context.Context, logger *slog.Logger, fn func() error) error {
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = fn()
		if err == nil {
			return nil
		}
		if !retryable(err) {
			return err
		}
		delay := time.Duration(1<<attempt)*time.Second + time.Duration(rand.Intn(500))*time.Millisecond
		logger.Warn("gmail transient error; backing off", "attempt", attempt+1, "delay", delay, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return err
}

func retryable(err error) bool {
	if err == nil {
		return false
	}
	if apiErr, ok := err.(*googleapi.Error); ok {
		switch apiErr.Code {
		case 429, 500, 502, 503, 504:
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "rate limit") || strings.Contains(msg, "quota") || strings.Contains(msg, "timeout")
}

func tokenFromFile(path string) (*oauth2.Token, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

func saveToken(path string, tok *oauth2.Token) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(tok)
}

func authCodeFlow(ctx context.Context, cfg *oauth2.Config) (*oauth2.Token, error) {
	cfg.RedirectURL = "urn:ietf:wg:oauth:2.0:oob"
	authURL := cfg.AuthCodeURL("state-token", oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	fmt.Println("Open the following URL in your browser, authorize access, then paste the code here:")
	fmt.Println(authURL)
	fmt.Print("Authorization code: ")
	var code string
	if _, err := fmt.Scanln(&code); err != nil {
		return nil, fmt.Errorf("read auth code: %w", err)
	}
	return cfg.Exchange(ctx, strings.TrimSpace(code))
}

// MockClient is an in-memory Gmail client for tests and dry environments.
type MockClient struct {
	Email        string
	mu           sync.Mutex
	Sent         []OutboundMessage
	Results      map[string]*SendResult
	Replies      map[string]bool
	FailNext     error
	AmbiguousNext bool
	SendCount    int
}

func NewMockClient(email string) *MockClient {
	return &MockClient{
		Email:   email,
		Results: map[string]*SendResult{},
		Replies: map[string]bool{},
	}
}

func (m *MockClient) SenderEmail() string { return m.Email }

func (m *MockClient) Send(ctx context.Context, msg OutboundMessage) (*SendResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailNext != nil {
		err := m.FailNext
		m.FailNext = nil
		return nil, err
	}
	if m.AmbiguousNext {
		m.AmbiguousNext = false
		return nil, fmt.Errorf("ambiguous send: connection reset after accept")
	}
	m.SendCount++
	id := fmt.Sprintf("msg-%d", m.SendCount)
	thread := msg.ThreadID
	if thread == "" {
		thread = fmt.Sprintf("thread-%d", m.SendCount)
	}
	res := &SendResult{MessageID: id, ThreadID: thread}
	m.Sent = append(m.Sent, msg)
	if msg.IdempotencyKey != "" {
		m.Results[msg.IdempotencyKey] = res
	}
	return res, nil
}

func (m *MockClient) HasReply(ctx context.Context, threadID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Replies[threadID], nil
}

func (m *MockClient) FindMessageByIdempotency(ctx context.Context, key string) (*SendResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Results[key], nil
}

func (m *MockClient) SetReply(threadID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Replies[threadID] = true
}

// Ensure unused import for http kept for oauth flows documentation.
var _ = http.StatusOK
