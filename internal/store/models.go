package store

import (
	"math"
	"time"
)

// User roles.
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// User statuses.
const (
	UserInvited  = "invited"
	UserActive   = "active"
	UserDisabled = "disabled"
)

// Token purposes.
const (
	TokenInvite = "invite"
	TokenReset  = "reset"
)

// Notification statuses.
const (
	NotificationPending   = "pending"
	NotificationSent      = "sent"
	NotificationFailed    = "failed"
	NotificationCancelled = "cancelled"
)

// Certificate statuses (computed, never stored).
const (
	StatusPending  = "pending"
	StatusOK       = "ok"
	StatusWarning  = "warning"
	StatusCritical = "critical"
	StatusExpired  = "expired"
	StatusError    = "error"
)

// User is an account that can sign in to the web interface. Alert
// recipients are a separate entity (Recipient).
type User struct {
	ID                int64      `json:"id"`
	Email             string     `json:"email"`
	Name              string     `json:"name"`
	Role              string     `json:"role"`
	Status            string     `json:"status"`
	PasswordHash      string     `json:"-"`
	FailedLogins      int        `json:"failed_logins"`
	LockedUntil       *time.Time `json:"locked_until"`
	PasswordChangedAt *time.Time `json:"password_changed_at"`
	LastLoginAt       *time.Time `json:"last_login_at"`
	InvitedBy         *int64     `json:"invited_by"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// IsAdmin reports whether the user has the admin role.
func (u *User) IsAdmin() bool { return u.Role == RoleAdmin }

// IsLocked reports whether the account is temporarily locked at time now.
func (u *User) IsLocked(now time.Time) bool {
	return u.LockedUntil != nil && u.LockedUntil.After(now)
}

// Session is a server side login session. ID is the SHA-256 of the cookie token.
type Session struct {
	ID         string
	UserID     int64
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	IP         string
	UserAgent  string
}

// UserToken is a single-use invite or password reset token.
type UserToken struct {
	ID        int64
	UserID    int64
	Purpose   string
	CreatedBy *int64
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
}

// Group binds certificates to the recipients that are alerted about them.
type Group struct {
	ID               int64     `json:"id"`
	Name             string    `json:"name"`
	Description      string    `json:"description"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	CertificateCount int       `json:"certificate_count"`
	RecipientCount   int       `json:"recipient_count"`
	ChannelCount     int       `json:"channel_count"`
}

// Recipient is an e-mail address that receives alerts of its groups.
type Recipient struct {
	ID        int64     `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	GroupIDs  []int64   `json:"group_ids"`
}

// ChainCert describes one certificate of the chain presented by a server.
type ChainCert struct {
	Subject           string    `json:"subject"`
	Issuer            string    `json:"issuer"`
	NotBefore         time.Time `json:"not_before"`
	NotAfter          time.Time `json:"not_after"`
	IsCA              bool      `json:"is_ca"`
	FingerprintSHA256 string    `json:"fingerprint_sha256"`
}

// Certificate is a monitored TLS endpoint together with the data of the
// certificate seen on the last successful check.
type Certificate struct {
	ID                 int64       `json:"id"`
	URL                string      `json:"url"`
	Host               string      `json:"host"`
	Port               int         `json:"port"`
	Name               string      `json:"name"`
	Notes              string      `json:"notes"`
	Enabled            bool        `json:"enabled"`
	CreatedBy          *int64      `json:"created_by"`
	CreatedAt          time.Time   `json:"created_at"`
	UpdatedAt          time.Time   `json:"updated_at"`
	LastCheckedAt      *time.Time  `json:"last_checked_at"`
	LastSuccessAt      *time.Time  `json:"last_success_at"`
	LastError          string      `json:"last_error"`
	SubjectCN          string      `json:"subject_cn"`
	SubjectOrg         string      `json:"subject_org"`
	IssuerCN           string      `json:"issuer_cn"`
	IssuerOrg          string      `json:"issuer_org"`
	SANs               []string    `json:"sans"`
	SerialNumber       string      `json:"serial_number"`
	FingerprintSHA256  string      `json:"fingerprint_sha256"`
	SignatureAlgorithm string      `json:"signature_algorithm"`
	KeyAlgorithm       string      `json:"key_algorithm"`
	KeyBits            int         `json:"key_bits"`
	NotBefore          *time.Time  `json:"not_before"`
	NotAfter           *time.Time  `json:"not_after"`
	Trusted            bool        `json:"trusted"`
	VerifyError        string      `json:"verify_error"`
	TLSVersion         string      `json:"tls_version"`
	ResolvedIP         string      `json:"resolved_ip"`
	Chain              []ChainCert `json:"chain"`
	GroupIDs           []int64     `json:"group_ids"`
}

// DaysLeft returns whole days until expiry (negative once expired), or
// false when no certificate has been fetched yet.
func (c *Certificate) DaysLeft(now time.Time) (int, bool) {
	if c.NotAfter == nil {
		return 0, false
	}
	return DaysUntil(*c.NotAfter, now), true
}

// DaysUntil returns the number of whole days from now until t. A
// certificate that expires in 36 hours has 1 day left; an expired one has a
// negative value.
func DaysUntil(t, now time.Time) int {
	return int(math.Floor(t.Sub(now).Hours() / 24))
}

// Status computes the display status of a certificate.
func (c *Certificate) Status(now time.Time, warningDays, criticalDays int) string {
	if c.NotAfter == nil {
		if c.LastError != "" {
			return StatusError
		}
		return StatusPending
	}
	if !now.Before(*c.NotAfter) {
		return StatusExpired
	}
	days := DaysUntil(*c.NotAfter, now)
	switch {
	case days <= criticalDays:
		return StatusCritical
	case days <= warningDays:
		return StatusWarning
	}
	return StatusOK
}

// CheckOutcome is the result of a TLS check to be persisted.
type CheckOutcome struct {
	CheckedAt time.Time
	// Error is set when the check failed; certificate fields are then ignored
	// and the data of the last successful check is kept.
	Error string

	SubjectCN          string
	SubjectOrg         string
	IssuerCN           string
	IssuerOrg          string
	SANs               []string
	SerialNumber       string
	FingerprintSHA256  string
	SignatureAlgorithm string
	KeyAlgorithm       string
	KeyBits            int
	NotBefore          time.Time
	NotAfter           time.Time
	Trusted            bool
	VerifyError        string
	TLSVersion         string
	ResolvedIP         string
	Chain              []ChainCert
}

// CheckTarget is the minimal data needed to check an endpoint.
type CheckTarget struct {
	ID   int64
	Host string
	Port int
}

// Notification is one alert for one recipient (e-mail) or one channel
// (messenger) about one certificate in one group at one threshold. It
// doubles as the delivery history.
type Notification struct {
	ID            int64  `json:"id"`
	GroupID       *int64 `json:"group_id"`
	CertificateID *int64 `json:"certificate_id"`
	RecipientID   *int64 `json:"recipient_id"`
	// ChannelType is set for messenger notifications ("" = e-mail); then
	// RecipientEmail holds the channel name.
	ChannelID      *int64     `json:"channel_id"`
	ChannelType    string     `json:"channel_type"`
	GroupName      string     `json:"group_name"`
	CertificateURL string     `json:"certificate_url"`
	RecipientEmail string     `json:"recipient_email"`
	NotAfter       time.Time  `json:"not_after"`
	ThresholdDays  int        `json:"threshold_days"`
	DaysLeft       int        `json:"days_left"`
	Status         string     `json:"status"`
	Attempts       int        `json:"attempts"`
	LastError      string     `json:"last_error"`
	NextAttemptAt  time.Time  `json:"next_attempt_at"`
	CreatedAt      time.Time  `json:"created_at"`
	SentAt         *time.Time `json:"sent_at"`
}

// AlertCandidate is a (certificate, group, recipient) triple whose
// certificate is close to expiry.
type AlertCandidate struct {
	CertificateID  int64
	CertificateURL string
	NotAfter       time.Time
	GroupID        int64
	GroupName      string
	RecipientID    int64
	RecipientEmail string
}

// DueNotification is a pending notification joined with the current state
// of the entities it refers to, so the sender can drop stale ones.
type DueNotification struct {
	Notification
	Valid bool // certificate, group and recipient or channel still match

	CertificateName string
	SubjectCN       string
	IssuerCN        string
	CurrentNotAfter *time.Time
	RecipientName   string
	GroupDesc       string
}

// AuditEntry records a security relevant or modifying action.
type AuditEntry struct {
	ID         int64     `json:"id"`
	UserID     *int64    `json:"user_id"`
	UserEmail  string    `json:"user_email"`
	Action     string    `json:"action"`
	EntityType string    `json:"entity_type"`
	EntityID   *int64    `json:"entity_id"`
	Details    string    `json:"details"`
	IP         string    `json:"ip"`
	CreatedAt  time.Time `json:"created_at"`
}
