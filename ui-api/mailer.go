package main

import (
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Mailer is intentionally tiny: the app only ever sends transactional
// messages (alerts and reports) to a handful of addresses. We don't want
// a mailer dependency, and the stdlib smtp client is enough.
type Mailer interface {
	Send(to []string, subject, body string) error
	SendWithAttachment(to []string, subject, body string, attachment Attachment) error
	Enabled() bool
}

type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

func newMailer(r *Resolved) Mailer {
	// SMTP_USER + SMTP_PASSWORD is the minimum viable config. Host
	// defaults to smtp.gmail.com and From defaults to SMTP_USER in
	// loadConfig, so a Gmail app password alone is enough.
	if r.SMTPUser == "" || r.SMTPPassword == "" {
		return &logMailer{}
	}
	return &smtpMailer{
		host: r.SMTPHost,
		port: r.SMTPPort,
		user: r.SMTPUser,
		pass: r.SMTPPassword,
		from: r.SMTPFrom,
	}
}

// logMailer is the no-op fallback that runs when SMTP isn't configured.
// Tests and local development flow through it without side effects.
type logMailer struct{}

func (l *logMailer) Enabled() bool { return false }
func (l *logMailer) Send(to []string, subject, body string) error {
	log.Printf("[mailer/noop] would send to=%v subject=%q bytes=%d", to, subject, len(body))
	return nil
}
func (l *logMailer) SendWithAttachment(to []string, subject, body string, a Attachment) error {
	log.Printf("[mailer/noop] would send to=%v subject=%q attach=%s (%d bytes)", to, subject, a.Filename, len(a.Data))
	return nil
}

type smtpMailer struct {
	host, port, user, pass, from string
}

func (s *smtpMailer) Enabled() bool { return true }

func (s *smtpMailer) Send(to []string, subject, body string) error {
	msg := buildSimpleMessage(s.from, to, subject, body)
	return s.dial(to, msg)
}

func (s *smtpMailer) SendWithAttachment(to []string, subject, body string, a Attachment) error {
	msg := buildMixedMessage(s.from, to, subject, body, a)
	return s.dial(to, msg)
}

func (s *smtpMailer) dial(to []string, msg []byte) error {
	addr := net.JoinHostPort(s.host, s.port)
	var auth smtp.Auth
	if s.user != "" {
		auth = smtp.PlainAuth("", s.user, s.pass, s.host)
	}
	return smtp.SendMail(addr, auth, s.from, to, msg)
}

func buildSimpleMessage(from string, to []string, subject, body string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}

// buildMixedMessage emits a tiny multipart/mixed: text body + one
// attachment. We base64-encode the attachment in 76-char lines, which is
// what the RFC requires and what every mail server expects.
func buildMixedMessage(from string, to []string, subject, body string, a Attachment) []byte {
	boundary := fmt.Sprintf("boundary_%d", time.Now().UnixNano())
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n")
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("\r\n")

	// body part
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	b.WriteString("\r\n")

	// attachment part
	ct := a.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: " + ct + "\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("Content-Disposition: attachment; filename=\"" + a.Filename + "\"\r\n")
	b.WriteString("\r\n")
	b.WriteString(chunkBase64(a.Data))
	b.WriteString("\r\n")

	b.WriteString("--" + boundary + "--\r\n")
	return []byte(b.String())
}

// chunkBase64 returns the input as base64 with CRLF every 76 chars, per
// RFC 2045.
func chunkBase64(data []byte) string {
	const lineLen = 76
	enc := base64.StdEncoding.EncodeToString(data)
	var b strings.Builder
	for i := 0; i < len(enc); i += lineLen {
		end := i + lineLen
		if end > len(enc) {
			end = len(enc)
		}
		b.WriteString(enc[i:end])
		b.WriteString("\r\n")
	}
	return b.String()
}
