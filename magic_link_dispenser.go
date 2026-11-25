// Package task issues single use email magic links, lets a mail scanner render
// the landing page without spending the link, and only exchanges the link for a
// session when the bound browser confirms.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Errors reported by the dispenser.
var (
	ErrBlankEmail       = errors.New("email is required")
	ErrBadEmail         = errors.New("email is malformed")
	ErrReturnTo         = errors.New("return path is not on the allowlist")
	ErrTokenNotFound    = errors.New("magic link token not found")
	ErrTokenUsed        = errors.New("magic link token was already redeemed")
	ErrTokenExpired     = errors.New("magic link token has expired")
	ErrTokenRevoked     = errors.New("magic link token was revoked")
	ErrBrowserMismatch  = errors.New("magic link does not belong to this browser")
	ErrActiveLinkLimit  = errors.New("active magic link limit reached for this email")
	ErrCooldown         = errors.New("magic link was sent too recently for this email")
	ErrNoActiveLink     = errors.New("email has no active magic link")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
	ErrEmptyAllowPrefix = errors.New("at least one return path prefix is required")
)

// Options bounds the dispenser and supplies its clock.
type Options struct {
	Now                    func() time.Time
	TTL                    time.Duration
	Cooldown               time.Duration
	MaxActiveLinksPerEmail int
	SessionTTL             time.Duration
	AllowedPrefixes        []string
}

// Link is one issued magic link.
type Link struct {
	Token       string    `json:"token"`
	Email       string    `json:"email"`
	ReturnTo    string    `json:"return_to"`
	BindCookie  string    `json:"bind_cookie"`
	IssuedAt    time.Time `json:"issued_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	ResendCount int       `json:"resend_count"`
	Used        bool      `json:"used"`
	Revoked     bool      `json:"revoked"`
	Seq         int64     `json:"seq"`
}

// Landing is what the confirmation page renders before the link is spent.
type Landing struct {
	Email       string    `json:"email"`
	MaskedEmail string    `json:"masked_email"`
	ReturnTo    string    `json:"return_to"`
	ConfirmPath string    `json:"confirm_path"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// Session is the signed in session produced by a successful redeem.
type Session struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	ReturnTo  string    `json:"return_to"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// MagicLinkDispenser owns the issued links, the cooldown table and the sequence.
type MagicLinkDispenser struct {
	opts Options
}

// NewMagicLinkDispenser builds a dispenser with the given options.
func NewMagicLinkDispenser(opts Options) *MagicLinkDispenser {
	return &MagicLinkDispenser{opts: opts}
}

// Send issues one magic link for an email and a return path.
func (d *MagicLinkDispenser) Send(email, returnTo string) (Link, error) {
	return Link{}, errNotImplemented
}

// Preview renders the confirmation page of one token without spending it.
func (d *MagicLinkDispenser) Preview(token string) (Landing, error) {
	return Landing{}, errNotImplemented
}

// Redeem exchanges one token for a session.
func (d *MagicLinkDispenser) Redeem(token, bindCookie string) (Session, error) {
	return Session{}, errNotImplemented
}

// Resend issues a fresh link for an email that already received one.
func (d *MagicLinkDispenser) Resend(email string) (Link, error) {
	return Link{}, errNotImplemented
}

// Revoke drops every usable link of one email.
func (d *MagicLinkDispenser) Revoke(email string) error {
	return errNotImplemented
}

// Links lists the links issued for one email, oldest first.
func (d *MagicLinkDispenser) Links(email string) []Link {
	return nil
}

// NormalizeEmail trims and lower cases one address.
func (d *MagicLinkDispenser) NormalizeEmail(email string) (string, error) {
	return "", errNotImplemented
}

// MaskEmail masks the local part of an address for display.
func (d *MagicLinkDispenser) MaskEmail(email string) string {
	return ""
}

// Snapshot serialises links, cooldown markers and the sequence.
func (d *MagicLinkDispenser) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole dispenser with a snapshot, all or nothing.
func (d *MagicLinkDispenser) Restore(data []byte) error {
	return errNotImplemented
}
