// Package middleware ports the Vapor backend's cross-cutting middleware.
package middleware

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIP resolves the address of the client behind our own proxies, as
// ClientIP.swift did: X-Forwarded-For is read right to left only when the
// direct peer is a trusted proxy, and the first hop outside the trusted
// ranges wins.
//
// One addition for web-v2, which runs on Cloudflare Workers and so reaches
// the API from outside the cluster: a request carrying X-Norviq-Edge-Key
// equal to EDGE_CLIENT_IP_KEY may name its client in X-Norviq-Client-IP.
// Without the key, that header is ignored.
type ClientIP struct {
	Trusted []netip.Prefix
	EdgeKey string
}

// Header names for the edge client-IP handoff.
const (
	HeaderEdgeClientIP = "X-Norviq-Client-IP"
	HeaderEdgeKey      = "X-Norviq-Edge-Key"
)

// NewClientIP parses a comma-separated CIDR list; bad entries are skipped.
func NewClientIP(cidrs, edgeKey string) *ClientIP {
	var trusted []netip.Prefix
	for _, raw := range strings.Split(cidrs, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if !strings.Contains(raw, "/") {
			if addr, err := parseAddr(raw); err == nil {
				trusted = append(trusted, netip.PrefixFrom(addr, addr.BitLen()))
			}
			continue
		}
		if p, err := netip.ParsePrefix(raw); err == nil {
			trusted = append(trusted, netip.PrefixFrom(p.Addr().Unmap(), bitsFor(p)).Masked())
		}
	}
	return &ClientIP{Trusted: trusted, EdgeKey: edgeKey}
}

func bitsFor(p netip.Prefix) int {
	if p.Addr().Is4In6() {
		return max(0, p.Bits()-96)
	}
	return p.Bits()
}

func parseAddr(s string) (netip.Addr, error) {
	a, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(s), "[]"))
	if err != nil {
		return netip.Addr{}, err
	}
	return a.Unmap(), nil
}

func (c *ClientIP) trusted(a netip.Addr) bool {
	for _, p := range c.Trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Resolve returns the client address for r, or "" when the peer is unknown.
func (c *ClientIP) Resolve(r *http.Request) string {
	if c.EdgeKey != "" {
		if key := r.Header.Get(HeaderEdgeKey); key != "" &&
			subtle.ConstantTimeCompare([]byte(key), []byte(c.EdgeKey)) == 1 {
			if ip := strings.TrimSpace(r.Header.Get(HeaderEdgeClientIP)); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return c.resolve(host, r.Header.Values("X-Forwarded-For"))
}

func (c *ClientIP) resolve(remote string, forwardedFor []string) string {
	if remote == "" {
		return ""
	}
	peer, err := parseAddr(remote)
	if err != nil || !c.trusted(peer) {
		return remote
	}
	var hops []string
	for _, header := range forwardedFor {
		for _, h := range strings.Split(header, ",") {
			if h = strings.TrimSpace(h); h != "" {
				hops = append(hops, h)
			}
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		addr, err := parseAddr(hops[i])
		if err != nil {
			// Not an address: the trusted proxy to its right was handed this,
			// so key on it rather than skip past it.
			return hops[i]
		}
		if !c.trusted(addr) {
			return hops[i]
		}
	}
	return remote
}
