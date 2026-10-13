package mailer

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
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
	if err := m.deliverSMTP(addr, auth, to, msg.Bytes()); err != nil {
		logger.Error("MAIL send failed to=%s subject=%q host=%s: %v", to, subject, addr, err)
		return fmt.Errorf("send mail: %w", err)
	}
	logger.Info("MAIL sent to=%s subject=%q", to, subject)
	return nil
}

// deliverSMTP sends one message with timeouts, using implicit TLS on port 465
// and STARTTLS wherever the server offers it, which net/smtp's SendMail does not
// do for 465 and never times out for.
func (m *Mailer) deliverSMTP(addr string, auth smtp.Auth, to string, msg []byte) error {
	host := m.cfg.SMTPHost
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	var err error
	tlsConf := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if m.cfg.SMTPPort == 465 {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsConf)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(45 * time.Second)); err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer c.Close()
	if m.cfg.SMTPPort != 465 {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsConf); err != nil {
				return err
			}
		}
	}
	if auth != nil {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(auth); err != nil {
				return err
			}
		}
	}
	if err := c.Mail(m.cfg.MailFrom); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
