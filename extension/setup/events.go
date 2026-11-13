// Package setup defines the small operator-only setup event stream. It is not
// an MCP extension and intentionally has no token, executable or browser socket.
package setup

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const Version = 1
const MaxLine = 16 << 10

const (
	PhasePrepareStore            = "prepare_store"
	PhaseConfigureApplication    = "configure_application"
	PhaseStartAuthorization      = "start_authorization"
	PhaseWaitForAuthorization    = "wait_for_authorization"
	PhaseVerifyAuthorization     = "verify_authorization"
	PhaseComplete                = "complete"
	CodePrepareStoreFailed       = "PREPARE_STORE_FAILED"
	CodeAppConfigFailed          = "APP_CONFIG_FAILED"
	CodeAuthStartFailed          = "AUTH_START_FAILED"
	CodeProjectCLIUnavailable    = "PROJECT_CLI_UNAVAILABLE"
	CodeProjectAuthNetworkFailed = "PROJECT_AUTH_NETWORK_FAILED"
	CodeProjectAuthTLSFailed     = "PROJECT_AUTH_TLS_FAILED"
	CodeAuthResponseUnsupported  = "AUTH_RESPONSE_UNSUPPORTED"
	CodeProjectResponseInvalid   = "PROJECT_AUTH_RESPONSE_INVALID"
	CodeProjectDeviceMissing     = "PROJECT_AUTH_DEVICE_MISSING"
	CodeProjectClientMissing     = "PROJECT_AUTH_CLIENT_MISSING"
	CodeProjectExpiryInvalid     = "PROJECT_AUTH_EXPIRY_INVALID"
	CodeProjectURLMissing        = "PROJECT_AUTH_URL_MISSING"
	CodeProjectUserCodeInvalid   = "PROJECT_AUTH_USER_CODE_INVALID"
	CodeProjectIntervalInvalid   = "PROJECT_AUTH_INTERVAL_INVALID"
	CodeAuthPollFailed           = "AUTH_POLL_FAILED"
	CodeAuthScopesIncomplete     = "AUTH_SCOPES_INCOMPLETE"
	CodeAuthVerifyNetworkFailed  = "AUTH_VERIFY_NETWORK_FAILED"
	CodeAuthVerifyIdentityFailed = "AUTH_VERIFY_IDENTITY_FAILED"
	CodeSetupCancelled           = "SETUP_CANCELLED"
	CodeSetupTimeout             = "SETUP_TIMEOUT"
)

type Event struct {
	Version         int       `json:"v"`
	Type            string    `json:"type"`
	Service         string    `json:"service,omitempty"`
	Message         string    `json:"message,omitempty"`
	VerificationURL string    `json:"verification_url,omitempty"`
	UserCode        string    `json:"user_code,omitempty"`
	ExpiresAt       time.Time `json:"expires_at,omitempty"`
	Authenticated   bool      `json:"authenticated,omitempty"`
	Phase           string    `json:"phase,omitempty"`
	Code            string    `json:"code,omitempty"`
	Retryable       bool      `json:"retryable,omitempty"`
	MissingScopes   []string  `json:"missing_scopes,omitempty"`
}

var scopeName = regexp.MustCompile(`^[a-z][a-z0-9_]*:[a-z0-9_.:-]{1,120}$`)

// AuthorizationCode accepts only a short operator-facing device user code.
// It is never a token or a model-facing result.
func AuthorizationCode(code string) bool {
	if len(code) == 0 || len(code) > 64 {
		return false
	}
	for _, r := range code {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == ' ') {
			return false
		}
	}
	return strings.TrimSpace(code) != ""
}

// FailureEvent returns one fixed, display-safe failure. The helper and host
// share this closed vocabulary so neither raw CLI stderr nor authorization
// values can become operator UI text.
func FailureEvent(phase, code string) (Event, error) {
	event := Event{Type: "failure", Phase: phase, Code: code}
	return SanitizeFailure(event)
}

// SanitizeFailure validates phase/code and replaces every caller-supplied
// display field with the canonical message and retry classification.
func SanitizeFailure(event Event) (Event, error) {
	wantPhase, message, retryable, ok := failureDefinition(event.Code)
	if !ok || event.Phase != wantPhase {
		return Event{}, errors.New("unsupported setup failure code or phase")
	}
	var missing []string
	if event.Code == CodeAuthScopesIncomplete {
		if len(event.MissingScopes) > 32 {
			return Event{}, errors.New("setup missing scopes exceed display budget")
		}
		for _, scope := range event.MissingScopes {
			if !scopeName.MatchString(scope) {
				return Event{}, errors.New("invalid setup scope name")
			}
			missing = append(missing, scope)
		}
	}
	return Event{
		Version: Version, Type: "failure", Service: event.Service,
		Phase: wantPhase, Code: event.Code, Message: message, Retryable: retryable,
		MissingScopes: missing,
	}, nil
}

func failureDefinition(code string) (phase, message string, retryable, ok bool) {
	switch code {
	case CodePrepareStoreFailed:
		return PhasePrepareStore, "Private authorization state could not be prepared.", false, true
	case CodeAppConfigFailed:
		return PhaseConfigureApplication, "Application configuration could not be verified.", false, true
	case CodeAuthStartFailed:
		return PhaseStartAuthorization, "User authorization could not be started.", true, true
	case CodeProjectCLIUnavailable:
		return PhaseStartAuthorization, "Project CLI could not be started. Check the saved native executable path and reapprove the source if it changed.", true, true
	case CodeProjectAuthNetworkFailed:
		return PhaseStartAuthorization, "Project authorization could not reach the configured host. Check the saved host, network and proxy settings.", true, true
	case CodeProjectAuthTLSFailed:
		return PhaseStartAuthorization, "Project authorization could not establish a trusted TLS connection. Check the configured host, network and system certificate trust.", true, true
	case CodeAuthResponseUnsupported:
		return PhaseStartAuthorization, "The authorization service returned an unsupported response.", false, true
	case CodeProjectResponseInvalid:
		return PhaseStartAuthorization, "Project CLI returned an unsupported authorization response format.", false, true
	case CodeProjectDeviceMissing:
		return PhaseStartAuthorization, "Project authorization response did not include a device code.", false, true
	case CodeProjectClientMissing:
		return PhaseStartAuthorization, "Project authorization response did not include a client ID.", false, true
	case CodeProjectExpiryInvalid:
		return PhaseStartAuthorization, "Project authorization response did not include a valid expiry.", false, true
	case CodeProjectURLMissing:
		return PhaseStartAuthorization, "Project authorization response did not include a verification URL.", false, true
	case CodeProjectUserCodeInvalid:
		return PhaseStartAuthorization, "Project authorization response did not include a usable user code for its verification URL.", false, true
	case CodeProjectIntervalInvalid:
		return PhaseStartAuthorization, "Project authorization returned an unsupported polling interval.", false, true
	case CodeAuthPollFailed:
		return PhaseWaitForAuthorization, "Authorization did not complete. Start a new authorization flow.", true, true
	case CodeAuthScopesIncomplete:
		return PhaseVerifyAuthorization, "Login completed and was verified, but some requested permissions were not granted. Granted permissions remain usable. Check application scopes, tenant approval and user consent before authorizing again.", false, true
	case CodeAuthVerifyNetworkFailed:
		return PhaseVerifyAuthorization, "Authorization may be saved, but server verification did not complete because of a network or service error.", true, true
	case CodeAuthVerifyIdentityFailed:
		return PhaseVerifyAuthorization, "Authorization was not verified as the required user identity.", false, true
	case CodeSetupCancelled:
		return PhaseComplete, "Authorization was cancelled.", true, true
	case CodeSetupTimeout:
		return PhaseComplete, "Authorization timed out. Start a new authorization flow.", true, true
	default:
		return "", "", false, false
	}
}

// AuthorizationURL requires a reviewed exact HTTPS origin. URL text, including
// a verification code, is only for the operator UI, never a model tool result.
func AuthorizationURL(raw string, origins []string) error {
	if len(raw) > 8192 || strings.ContainsAny(raw, "\x00\r\n") {
		return errors.New("invalid authorization URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return errors.New("authorization URL must be HTTPS without userinfo or fragment")
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return errors.New("local authorization destinations are refused")
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast()) {
		return errors.New("local authorization destinations are refused")
	}
	for _, rawOrigin := range origins {
		o, e := url.Parse(rawOrigin)
		if e == nil && o.Scheme == u.Scheme && strings.EqualFold(o.Host, u.Host) {
			return nil
		}
	}
	return errors.New("authorization origin is not reviewed; add its exact HTTPS origin to setup_origins and reapprove the source")
}
