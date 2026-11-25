// Package task issues single use email magic links, lets a mail scanner render
// the landing page without spending the link, and only exchanges the link for a
// session when the bound browser confirms.
package task

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

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
	mu           sync.Mutex
	opts         Options
	clock        func() time.Time
	links        map[string][]*Link
	byToken      map[string]*Link
	byCookie     map[string]*Link
	lastSent     map[string]time.Time
	resendCounts map[string]int
	nextSeq      int64
}

// NewMagicLinkDispenser builds a dispenser with the given options.
func NewMagicLinkDispenser(opts Options) *MagicLinkDispenser {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	opts.AllowedPrefixes = append([]string(nil), opts.AllowedPrefixes...)
	return &MagicLinkDispenser{
		opts:         opts,
		clock:        opts.Now,
		links:        make(map[string][]*Link),
		byToken:      make(map[string]*Link),
		byCookie:     make(map[string]*Link),
		lastSent:     make(map[string]time.Time),
		resendCounts: make(map[string]int),
	}
}

// Send issues one magic link for an email and a return path.
func (d *MagicLinkDispenser) Send(email, returnTo string) (Link, error) {
	normalized, err := d.NormalizeEmail(email)
	if err != nil {
		return Link{}, err
	}
	if err := d.validateReturnTo(returnTo); err != nil {
		return Link{}, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	now := d.clock()
	if d.opts.Cooldown > 0 {
		if last, ok := d.lastSent[normalized]; ok && now.Sub(last) < d.opts.Cooldown {
			return Link{}, ErrCooldown
		}
	}
	if d.opts.MaxActiveLinksPerEmail > 0 &&
		d.activeCountLocked(normalized, now) >= d.opts.MaxActiveLinksPerEmail {
		return Link{}, ErrActiveLinkLimit
	}

	link, err := d.issueLocked(normalized, returnTo, 0, now)
	if err != nil {
		return Link{}, err
	}
	delete(d.resendCounts, normalized)
	return *link, nil
}

// Preview renders the confirmation page of one token without spending it.
func (d *MagicLinkDispenser) Preview(token string) (Landing, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	link, ok := d.byToken[token]
	if !ok {
		return Landing{}, ErrTokenNotFound
	}
	if link.Used {
		return Landing{}, ErrTokenUsed
	}
	if link.Revoked {
		return Landing{}, ErrTokenRevoked
	}
	if !d.clock().Before(link.ExpiresAt) {
		return Landing{}, ErrTokenExpired
	}

	return Landing{
		Email:       link.Email,
		MaskedEmail: d.MaskEmail(link.Email),
		ReturnTo:    link.ReturnTo,
		ConfirmPath: "/magic-link/confirm?token=" + url.QueryEscape(link.Token),
		ExpiresAt:   link.ExpiresAt,
	}, nil
}

// Redeem exchanges one token for a session.
func (d *MagicLinkDispenser) Redeem(token, bindCookie string) (Session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	link, ok := d.byToken[token]
	if !ok {
		return Session{}, ErrTokenNotFound
	}
	if bindCookie == "" ||
		subtle.ConstantTimeCompare([]byte(bindCookie), []byte(link.BindCookie)) != 1 {
		return Session{}, ErrBrowserMismatch
	}
	if link.Used {
		return Session{}, ErrTokenUsed
	}
	if link.Revoked {
		return Session{}, ErrTokenRevoked
	}

	now := d.clock()
	if !now.Before(link.ExpiresAt) {
		return Session{}, ErrTokenExpired
	}

	sessionID, err := randomValue(32)
	if err != nil {
		return Session{}, err
	}
	for sessionID == link.Token {
		sessionID, err = randomValue(32)
		if err != nil {
			return Session{}, err
		}
	}

	link.Used = true
	return Session{
		ID:        sessionID,
		Email:     link.Email,
		ReturnTo:  link.ReturnTo,
		IssuedAt:  now,
		ExpiresAt: now.Add(d.opts.SessionTTL),
	}, nil
}

// Resend issues a fresh link for an email that already received one.
func (d *MagicLinkDispenser) Resend(email string) (Link, error) {
	normalized, err := d.NormalizeEmail(email)
	if err != nil {
		return Link{}, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	now := d.clock()
	var latest *Link
	for _, link := range d.links[normalized] {
		if d.activeLocked(link, now) && (latest == nil || link.Seq > latest.Seq) {
			latest = link
		}
	}
	if latest == nil {
		return Link{}, ErrNoActiveLink
	}

	for _, link := range d.links[normalized] {
		if !link.Used {
			link.Revoked = true
		}
	}
	d.resendCounts[normalized]++
	returnTo := latest.ReturnTo
	link, err := d.issueLocked(normalized, returnTo, d.resendCounts[normalized], now)
	if err != nil {
		return Link{}, err
	}
	return *link, nil
}

// Revoke drops every usable link of one email.
func (d *MagicLinkDispenser) Revoke(email string) error {
	normalized, err := d.NormalizeEmail(email)
	if err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	for _, link := range d.links[normalized] {
		link.Revoked = true
	}
	delete(d.lastSent, normalized)
	return nil
}

// Links lists the links issued for one email, oldest first.
func (d *MagicLinkDispenser) Links(email string) []Link {
	normalized, err := d.NormalizeEmail(email)
	if err != nil {
		return []Link{}
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	stored := d.links[normalized]
	out := make([]Link, len(stored))
	for i, link := range stored {
		out[i] = *link
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// NormalizeEmail trims and lower cases one address.
func (d *MagicLinkDispenser) NormalizeEmail(email string) (string, error) {
	return normalizeEmail(email)
}

// MaskEmail masks the local part of an address for display.
func (d *MagicLinkDispenser) MaskEmail(email string) string {
	trimmed := strings.TrimSpace(email)
	if strings.Count(trimmed, "@") != 1 {
		return ""
	}
	local, domain, _ := strings.Cut(trimmed, "@")
	if local == "" || domain == "" {
		return ""
	}
	runes := []rune(local)
	if len(runes) == 1 {
		return string(runes[0]) + "***@" + domain
	}
	return string(runes[0]) + "***" + string(runes[len(runes)-1]) + "@" + domain
}

// Snapshot serialises links, cooldown markers and the sequence.
func (d *MagicLinkDispenser) Snapshot() ([]byte, error) {
	d.mu.Lock()
	payload := snapshotPayload{
		Version:      snapshotVersion,
		NextSeq:      d.nextSeq,
		Links:        make(map[string][]Link, len(d.links)),
		Cooldown:     make(map[string]time.Time, len(d.lastSent)),
		ResendCounts: make(map[string]int, len(d.resendCounts)),
	}
	for email, links := range d.links {
		copied := make([]Link, len(links))
		for i, link := range links {
			copied[i] = *link
		}
		payload.Links[email] = copied
	}
	for email, at := range d.lastSent {
		payload.Cooldown[email] = at
	}
	for email, count := range d.resendCounts {
		payload.ResendCounts[email] = count
	}
	d.mu.Unlock()

	checksum, err := payload.checksum()
	if err != nil {
		return nil, err
	}
	payload.Checksum = checksum
	return json.Marshal(payload)
}

// Restore replaces the whole dispenser with a snapshot, all or nothing.
func (d *MagicLinkDispenser) Restore(data []byte) error {
	payload, links, byToken, byCookie, err := parseSnapshot(data)
	if err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.links = links
	d.byToken = byToken
	d.byCookie = byCookie
	d.lastSent = payload.Cooldown
	d.resendCounts = payload.ResendCounts
	d.nextSeq = payload.NextSeq
	if d.lastSent == nil {
		d.lastSent = make(map[string]time.Time)
	}
	if d.resendCounts == nil {
		d.resendCounts = make(map[string]int)
	}
	return nil
}

const snapshotVersion = 1

type snapshotPayload struct {
	Version      int                  `json:"version"`
	NextSeq      int64                `json:"next_seq"`
	Links        map[string][]Link    `json:"links"`
	Cooldown     map[string]time.Time `json:"cooldown"`
	ResendCounts map[string]int       `json:"resend_counts"`
	Checksum     string               `json:"checksum"`
}

func (p snapshotPayload) checksum() (string, error) {
	p.Checksum = ""
	encoded, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func (d *MagicLinkDispenser) validateReturnTo(returnTo string) error {
	validPrefixes := 0
	for _, prefix := range d.opts.AllowedPrefixes {
		if prefix != "" && strings.HasPrefix(prefix, "/") && !strings.HasPrefix(prefix, "//") {
			validPrefixes++
		}
	}
	if validPrefixes == 0 {
		return ErrEmptyAllowPrefix
	}
	if returnTo == "" || !strings.HasPrefix(returnTo, "/") ||
		strings.HasPrefix(returnTo, "//") || strings.Contains(returnTo, "\\") {
		return ErrReturnTo
	}

	parsed, err := url.Parse(returnTo)
	if err != nil || parsed.IsAbs() || parsed.Host != "" ||
		strings.HasPrefix(parsed.Path, "//") {
		return ErrReturnTo
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == ".." {
			return ErrReturnTo
		}
	}
	for _, prefix := range d.opts.AllowedPrefixes {
		if prefix != "" && strings.HasPrefix(prefix, "/") &&
			!strings.HasPrefix(prefix, "//") && strings.HasPrefix(parsed.Path, prefix) {
			return nil
		}
	}
	return ErrReturnTo
}

func (d *MagicLinkDispenser) activeLocked(link *Link, now time.Time) bool {
	return !link.Used && !link.Revoked && now.Before(link.ExpiresAt)
}

func (d *MagicLinkDispenser) activeCountLocked(email string, now time.Time) int {
	count := 0
	for _, link := range d.links[email] {
		if d.activeLocked(link, now) {
			count++
		}
	}
	return count
}

func (d *MagicLinkDispenser) issueLocked(email, returnTo string, resendCount int, now time.Time) (*Link, error) {
	token, err := d.uniqueValueLocked(d.byToken)
	if err != nil {
		return nil, err
	}
	cookie, err := d.uniqueValueLocked(d.byCookie)
	if err != nil {
		return nil, err
	}
	for cookie == token {
		cookie, err = d.uniqueValueLocked(d.byCookie)
		if err != nil {
			return nil, err
		}
	}

	d.nextSeq++
	link := &Link{
		Token:       token,
		Email:       email,
		ReturnTo:    returnTo,
		BindCookie:  cookie,
		IssuedAt:    now,
		ExpiresAt:   now.Add(d.opts.TTL),
		ResendCount: resendCount,
		Seq:         d.nextSeq,
	}
	d.links[email] = append(d.links[email], link)
	d.byToken[token] = link
	d.byCookie[cookie] = link
	d.lastSent[email] = now
	return link, nil
}

func (d *MagicLinkDispenser) uniqueValueLocked(existing map[string]*Link) (string, error) {
	for {
		value, err := randomValue(32)
		if err != nil {
			return "", err
		}
		if _, ok := existing[value]; !ok {
			return value, nil
		}
	}
}

func randomValue(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func parseSnapshot(data []byte) (snapshotPayload, map[string][]*Link, map[string]*Link, map[string]*Link, error) {
	var payload snapshotPayload
	empty := func() (snapshotPayload, map[string][]*Link, map[string]*Link, map[string]*Link, error) {
		return snapshotPayload{}, nil, nil, nil, ErrBadSnapshot
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return empty()
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&payload); err != nil {
		return empty()
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return empty()
	}
	if payload.Version != snapshotVersion || payload.NextSeq < 0 {
		return empty()
	}
	expected, err := payload.checksum()
	if err != nil || expected != payload.Checksum {
		return empty()
	}

	links := make(map[string][]*Link)
	byToken := make(map[string]*Link)
	byCookie := make(map[string]*Link)
	seenSeq := make(map[int64]struct{})
	for email, stored := range payload.Links {
		normalized, err := normalizeEmail(email)
		if err != nil || normalized != email {
			return empty()
		}
		copied := make([]*Link, 0, len(stored))
		for _, item := range stored {
			if item.Token == "" || item.BindCookie == "" ||
				item.Seq <= 0 || item.Seq > payload.NextSeq ||
				item.ResendCount < 0 {
				return empty()
			}
			normalizedLinkEmail, err := normalizeEmail(item.Email)
			if err != nil || normalizedLinkEmail != email {
				return empty()
			}
			if _, ok := byToken[item.Token]; ok {
				return empty()
			}
			if _, ok := byCookie[item.BindCookie]; ok {
				return empty()
			}
			if _, ok := seenSeq[item.Seq]; ok {
				return empty()
			}
			seenSeq[item.Seq] = struct{}{}

			link := item
			copied = append(copied, &link)
			byToken[link.Token] = &link
			byCookie[link.BindCookie] = &link
		}
		sort.SliceStable(copied, func(i, j int) bool { return copied[i].Seq < copied[j].Seq })
		links[email] = copied
	}

	cooldown := make(map[string]time.Time, len(payload.Cooldown))
	for email, at := range payload.Cooldown {
		normalized, err := normalizeEmail(email)
		if err != nil || normalized != email {
			return empty()
		}
		cooldown[email] = at
	}
	resendCounts := make(map[string]int, len(payload.ResendCounts))
	for email, count := range payload.ResendCounts {
		normalized, err := normalizeEmail(email)
		if err != nil || normalized != email || count < 0 {
			return empty()
		}
		resendCounts[email] = count
	}
	payload.Cooldown = cooldown
	payload.ResendCounts = resendCounts
	return payload, links, byToken, byCookie, nil
}

func normalizeEmail(email string) (string, error) {
	trimmed := strings.TrimSpace(email)
	if trimmed == "" {
		return "", ErrBlankEmail
	}
	normalized := strings.ToLower(trimmed)
	if strings.Count(normalized, "@") != 1 {
		return "", ErrBadEmail
	}
	local, domain, _ := strings.Cut(normalized, "@")
	if local == "" || domain == "" {
		return "", ErrBadEmail
	}
	if strings.ContainsAny(local+domain, " \t\r\n") {
		return "", ErrBadEmail
	}
	return normalized, nil
}
