package gmail

import (
	"encoding/base64"
	"fmt"
	"strings"
)

func buildMIME(from string, msg OutboundMessage) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("From: %s\r\n", from))
	b.WriteString(fmt.Sprintf("To: %s\r\n", msg.To))
	b.WriteString(fmt.Sprintf("Subject: %s\r\n", sanitizeHeader(msg.Subject)))
	b.WriteString("MIME-Version: 1.0\r\n")
	if msg.IdempotencyKey != "" {
		b.WriteString(fmt.Sprintf("X-Outreach-Idempotency: %s\r\n", sanitizeHeader(msg.IdempotencyKey)))
	}
	if msg.InReplyTo != "" {
		b.WriteString(fmt.Sprintf("In-Reply-To: %s\r\n", sanitizeHeader(msg.InReplyTo)))
		b.WriteString(fmt.Sprintf("References: %s\r\n", sanitizeHeader(msg.InReplyTo)))
	}

	if strings.TrimSpace(msg.BodyHTML) != "" {
		boundary := "outreach_boundary_7a3f"
		b.WriteString(fmt.Sprintf("Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary))
		b.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		b.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n\r\n")
		b.WriteString(msg.BodyText)
		b.WriteString("\r\n")
		b.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		b.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n\r\n")
		b.WriteString(msg.BodyHTML)
		b.WriteString("\r\n")
		b.WriteString(fmt.Sprintf("--%s--", boundary))
	} else {
		b.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n\r\n")
		b.WriteString(msg.BodyText)
	}

	return base64.URLEncoding.EncodeToString([]byte(b.String()))
}

func sanitizeHeader(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
