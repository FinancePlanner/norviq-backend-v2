// Package outbound is OutboundURLGuard (Shared/OutboundURLGuard.swift): the
// one check every request to a user-supplied URL goes through.
//
// Bring-your-own-key lets a user hand the server a base URL it will then send
// a bearer token to. Unguarded, that is SSRF and credential exfiltration:
// point it at 169.254.169.254 or an internal host and it authenticates there.
// Call Validate at use time as well as at create time; a host that resolved
// to a public address when it was saved can point elsewhere later.
package outbound

import (
	"net/url"
	"strconv"
	"strings"

	"norviq-api/internal/httpx"
)

// Failure is OutboundURLGuard.Failure; Error() is its description.
type Failure struct {
	kind string
	host string
}

// The three failure kinds.
var (
	ErrNotHTTPS  = &Failure{kind: "notHTTPS"}
	ErrMalformed = &Failure{kind: "malformed"}
)

func (f *Failure) Error() string {
	switch f.kind {
	case "notHTTPS":
		return "URL must use https."
	case "malformed":
		return "URL is not a valid absolute URL."
	}
	return "Host " + f.host + " is not allowed."
}

// IsBlockedHost is true for hosts server-side code must never reach:
// loopback, link-local (including the cloud metadata endpoint), and the
// RFC 1918 private ranges. It matches on the host text, as Swift did.
func IsBlockedHost(host string) bool {
	host = strings.ToLower(host)
	switch {
	case host == "localhost" || strings.HasSuffix(host, ".local"):
		return true
	case host == "0.0.0.0" || host == "127.0.0.1" || host == "::1":
		return true
	case strings.HasPrefix(host, "127."):
		return true
	case strings.HasPrefix(host, "10.") || strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "169.254."):
		return true
	case strings.HasPrefix(host, "172."):
		parts := strings.Split(host, ".")
		if len(parts) > 1 {
			if second, err := strconv.Atoi(parts[1]); err == nil && second >= 16 && second <= 31 {
				return true
			}
		}
	}
	return false
}

// Validate checks a user-supplied base URL: absolute, https, allowed host.
func Validate(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return nil, ErrMalformed
	}
	if strings.ToLower(u.Scheme) != "https" {
		return nil, ErrNotHTTPS
	}
	// Foundation's URL.host keeps IPv6 literals without brackets, as
	// Hostname() does.
	if host := u.Hostname(); IsBlockedHost(host) {
		return nil, &Failure{kind: "blockedHost", host: host}
	}
	return u, nil
}

// ValidateForRequest is validateForRequest: the same check as a 400 for use
// inside a handler.
func ValidateForRequest(raw string) (*url.URL, error) {
	u, err := Validate(raw)
	if err != nil {
		return nil, httpx.BadRequest(err.Error())
	}
	return u, nil
}
