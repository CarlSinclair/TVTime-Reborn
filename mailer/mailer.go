package mailer

import (
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"strings"

	shared "github.com/CarlSinclair/csuit-libs/mailer"

	"tvt/config"
	"tvt/logger"
)

var ErrNotConfigured = errors.New("mail is not configured")

type Mailer struct {
	cfg *config.Config
	// Record, when set, is told the outcome of every send attempt.
	Record func(to, subject string, err error)
	smtp   *shared.Mailer
}

func New(cfg *config.Config) *Mailer {
	m := &Mailer{cfg: cfg}
	m.smtp = shared.New(func() (shared.Config, error) {
		return shared.Config{
			Host: cfg.SMTPHost,
			Port: strconv.Itoa(cfg.SMTPPort),
			User: cfg.SMTPUser,
			Pass: cfg.SMTPPass,
		}, nil
	})
	return m
}

func (m *Mailer) Configured() bool {
	return m.cfg.SMTPHost != "" && m.cfg.MailFrom != ""
}

func (m *Mailer) Send(to, subject, body string) error {
	return m.SendHTML(to, subject, body, "")
}

// SendHTML sends a multipart/alternative message when htmlBody is set,
// otherwise plain text.
func (m *Mailer) SendHTML(to, subject, textBody, htmlBody string) error {
	err := m.deliver(to, subject, textBody, htmlBody)
	if m.Record != nil {
		m.Record(to, subject, err)
	}
	return err
}

func (m *Mailer) deliver(to, subject, textBody, htmlBody string) error {
	if m.cfg.DevLogMail {
		logger.Info("MAIL (dev) to=%s subject=%q body=%s", to, subject, textBody)
		return nil
	}
	if !m.Configured() {
		logger.Error("MAIL not configured, cannot send subject=%q to=%s", subject, to)
		return ErrNotConfigured
	}
	from := shared.Sender{Address: strings.TrimSpace(m.cfg.MailFrom)}
	if parsed, err := mail.ParseAddress(m.cfg.MailFrom); err == nil {
		from = shared.Sender{Name: parsed.Name, Address: parsed.Address}
	}
	err := m.smtp.Send(shared.Message{To: to, From: from, Subject: subject, Text: textBody, HTML: htmlBody})
	if err != nil {
		logger.Error("MAIL send failed to=%s subject=%q host=%s:%d: %v", to, subject, m.cfg.SMTPHost, m.cfg.SMTPPort, err)
		return fmt.Errorf("send mail: %w", err)
	}
	logger.Info("MAIL sent to=%s subject=%q", to, subject)
	return nil
}
