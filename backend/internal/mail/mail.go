// Package mail sends transactional email (password reset). With SMTP
// configured it delivers through net/smtp; otherwise it only logs the message
// so local development works without a mail server.
package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Sender delivers a plain-text email.
type Sender interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Config is the SMTP connection; an empty Host selects the log-only sender.
type Config struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
}

// New returns an SMTP sender, or a logging one when cfg.Host is empty.
func New(cfg Config, log *slog.Logger) Sender {
	if cfg.Host == "" {
		log.Warn("SMTP_HOST not set: emails will be logged, not sent")
		return logSender{log: log}
	}
	return &smtpSender{cfg: cfg}
}

type logSender struct{ log *slog.Logger }

func (s logSender) Send(_ context.Context, to, subject, body string) error {
	s.log.Info("email not sent (SMTP not configured)", "to", to, "subject", subject, "body", body)
	return nil
}

type smtpSender struct{ cfg Config }

// Send uses implicit TLS on port 465 and opportunistic STARTTLS otherwise
// (required when credentials are set, since net/smtp refuses PLAIN auth over
// an unencrypted connection to a non-local host).
func (s *smtpSender) Send(ctx context.Context, to, subject, body string) error {
	if strings.ContainsAny(to+subject, "\r\n") {
		return errors.New("mail: header contains a newline")
	}
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	dialer := &net.Dialer{Deadline: deadline}
	tlsCfg := &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}

	var conn net.Conn
	var err error
	if s.cfg.Port == 465 {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("mail: dial: %w", err)
	}
	_ = conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("mail: hello: %w", err)
	}
	defer c.Close()

	if s.cfg.Port != 465 {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("mail: starttls: %w", err)
			}
		}
	}
	if s.cfg.User != "" {
		if err := c.Auth(smtp.PlainAuth("", s.cfg.User, s.cfg.Password, s.cfg.Host)); err != nil {
			return fmt.Errorf("mail: auth: %w", err)
		}
	}
	if err := c.Mail(s.cfg.From); err != nil {
		return fmt.Errorf("mail: from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("mail: rcpt: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: data: %w", err)
	}
	if _, err := w.Write(buildMessage(s.cfg.From, to, subject, body)); err != nil {
		return fmt.Errorf("mail: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: send: %w", err)
	}
	return c.Quit()
}

func buildMessage(from, to, subject, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))
	return []byte(b.String())
}
