package mailer

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/smtp"
	"strings"
	"time"

	"tvt/config"
	"tvt/logger"
)

var ErrNotConfigured = errors.New("mail is not configured")

type Mailer struct {
	cfg *config.Config
	// Record, when set, is told the outcome of every send attempt.
	Record func(to, subject string, err error)
}

func New(cfg *config.Config) *Mailer { return &Mailer{cfg: cfg} }

func (m *Mailer) Configured() bool {
	return m.cfg.SMTPHost != "" && m.cfg.MailFrom != ""
}

func clean(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

func qp(s string) string {
	var buf bytes.Buffer
	w := quotedprintable.NewWriter(&buf)
	w.Write([]byte(s))
	w.Close()
	return buf.String()
}

func (m *Mailer) Send(to, subject, body string) error {
	return m.SendHTML(to, subject, body, "")
}

// SendHTML sends a multipart/alternative message when htmlBody is set,
// otherwise plain text. Subject and bodies are encoded so non-ASCII languages
// survive any relay.
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
	var msg bytes.Buffer
	msg.WriteString("From: " + clean(m.cfg.MailFrom) + "\r\n")
	msg.WriteString("To: " + clean(to) + "\r\n")
	msg.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", clean(subject)) + "\r\n")
	msg.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	msg.WriteString("MIME-Version: 1.0\r\n")
	if htmlBody == "" {
		msg.WriteString("Content-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
		msg.WriteString(qp(textBody))
	} else {
		rb := make([]byte, 12)
		if _, err := rand.Read(rb); err != nil {
			return fmt.Errorf("mail boundary: %w", err)
		}
		boundary := "tvt" + hex.EncodeToString(rb)
		msg.WriteString("Content-Type: multipart/alternative; boundary=" + boundary + "\r\n\r\n")
		msg.WriteString("--" + boundary + "\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" + qp(textBody) + "\r\n")
		msg.WriteString("--" + boundary + "\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" + qp(htmlBody) + "\r\n")
		msg.WriteString("--" + boundary + "--\r\n")
	}
	addr := fmt.Sprintf("%s:%d", m.cfg.SMTPHost, m.cfg.SMTPPort)
	var auth smtp.Auth
	if m.cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", m.cfg.SMTPUser, m.cfg.SMTPPass, m.cfg.SMTPHost)
	}
	if err := smtp.SendMail(addr, auth, m.cfg.MailFrom, []string{to}, msg.Bytes()); err != nil {
		logger.Error("MAIL send failed to=%s subject=%q host=%s: %v", to, subject, addr, err)
		return fmt.Errorf("send mail: %w", err)
	}
	logger.Info("MAIL sent to=%s subject=%q", to, subject)
	return nil
}
