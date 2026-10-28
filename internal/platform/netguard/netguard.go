// Package netguard is OutboundURLGuard: it guards outbound requests to
// user-supplied URLs. Bring-your-own-key lets a user hand the server a base
// URL it will then send a bearer token to; unguarded, that is SSRF and
// credential exfiltration (cloud metadata, loopback, internal hosts).
//
// Call it at use time as well as at save time: a host that resolved to a
// public address when it was saved can point elsewhere later.
package netguard

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

func (f *Failure) Error() string {
	switch f.kind {
	case "notHTTPS":
		return "URL must use https."
	case "blockedHost":
		return "Host " + f.host + " is not allowed."
	}
	return "URL is not a valid absolute URL."
}

// IsBlockedHost is true for loopback, link-local (the cloud metadata
// endpoint included), `.local` and the RFC 1918 private ranges.
func IsBlockedHost(host string) bool {
	host = strings.ToLower(host)
	if host == "localhost" || strings.HasSuffix(host, ".local") {
		return true
	}
	if host == "0.0.0.0" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	if strings.HasPrefix(host, "127.") {
		return true
	}
	if strings.HasPrefix(host, "10.") || strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "169.254.") {
		return true
	}
	if strings.HasPrefix(host, "172.") {
		parts := strings.Split(host, ".")
		if len(parts) > 1 {
			if second, err := strconv.Atoi(parts[1]); err == nil && second >= 16 && second <= 31 {
				return true
			}
		}
	}
	return false
}

// Validate checks a user-supplied base URL: absolute, https, not a blocked host.
func Validate(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err != nil || strings.ContainsAny(trimmed, " \t\n") {
		return nil, &Failure{kind: "malformed"}
	}
	host := u.Hostname()
	if host == "" {
		return nil, &Failure{kind: "malformed"}
	}
	if strings.ToLower(u.Scheme) != "https" {
		return nil, &Failure{kind: "notHTTPS"}
	}
	if IsBlockedHost(host) {
		return nil, &Failure{kind: "blockedHost", host: host}
	}
	return u, nil
}

// ValidateForRequest is Validate surfaced as a 400 with the failure's text.
func ValidateForRequest(raw string) (*url.URL, error) {
	u, err := Validate(raw)
	if err != nil {
		return nil, httpx.BadRequest(err.Error())
	}
	return u, nil
}
