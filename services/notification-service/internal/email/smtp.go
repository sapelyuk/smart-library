// Package email implements the SMTP transport for sending notifications.
//
// It connects to an SMTP server and sends plain-text emails. The package is
// deliberately simple: it does not handle TLS certificate verification
// beyond the standard library defaults, and it does not support attachments
// or HTML. That is enough for reminder emails.
package email

import (
	"context"
	"fmt"
	"log/slog"
	"net/smtp"
	"strings"
	"time"
)

// Sender delivers a single email through SMTP.
type Sender struct {
	host     string
	port     int
	username string
	password string
	from     string
	log      *slog.Logger
}

// Config holds the SMTP connection parameters.
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

// New builds a sender from the given configuration.
func New(cfg Config, log *slog.Logger) *Sender {
	if log == nil {
		log = slog.Default()
	}

	return &Sender{
		host:     cfg.Host,
		port:     cfg.Port,
		username: cfg.Username,
		password: cfg.Password,
		from:     cfg.From,
		log:      log,
	}
}

// Send delivers the message to the given recipient.
func (s *Sender) Send(ctx context.Context, to, subject, body string) error {
	addr := smtpAddress(s.host, s.port)

	auth := smtp.PlainAuth("", s.username, s.password, s.host)

	msg := buildMessage(s.from, to, subject, body)

	const maxRetries = 3

	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := smtp.SendMail(addr, auth, s.from, []string{to}, msg); err != nil {
			lastErr = fmt.Errorf("smtp send (attempt %d): %w", attempt, err)
			s.log.WarnContext(ctx, "smtp send failed, retrying",
				"to", to, "attempt", attempt, "error", err)

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
				continue
			}
		}

		s.log.InfoContext(ctx, "email sent", "to", to, "subject", subject)

		return nil
	}

	return fmt.Errorf("email: %w: %v", ErrDeliveryFailed, lastErr)
}

// buildMessage assembles a plain-text email with the required headers.
func buildMessage(from, to, subject, body string) []byte {
	lines := []string{
		fmt.Sprintf("From: %s", from),
		fmt.Sprintf("To: %s", to),
		fmt.Sprintf("Subject: %s", subject),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		body,
	}

	return []byte(strings.Join(lines, "\r\n"))
}

// smtpAddress joins the host and port into a dial address.
func smtpAddress(host string, port int) string {
	return fmt.Sprintf("%s:%d", host, port)
}
