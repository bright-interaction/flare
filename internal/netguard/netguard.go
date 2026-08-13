// Package netguard centralizes the SSRF destination-blocking rule so every outbound
// tenant-influenced request (BYOAI base_url, alert webhooks, OIDC discovery) applies the
// same list and it cannot drift between call sites.
package netguard

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// reserved holds ranges the Go stdlib private/loopback checks do NOT cover. 100.64.0.0/10
// is carrier-grade NAT, which is ALSO the Tailscale tailnet range: Flare runs on a
// Tailscale node next to internal services (Dockyard, Hephaestus, vault APIs), so a guard
// that only calls net.IP.IsPrivate() would let a tenant URL reach them.
var reserved = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{
		"100.64.0.0/10", // CGNAT / Tailscale
		"192.0.0.0/24",  // IETF protocol assignments
		"198.18.0.0/15", // benchmarking
		"240.0.0.0/4",   // reserved / future use
	} {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// IsBlockedIP reports whether ip must not be dialed by an SSRF-guarded request: nil,
// loopback, RFC1918/ULA private, unspecified, link-local, or one of the reserved/CGNAT
// (Tailscale) ranges the stdlib helpers omit.
func IsBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, n := range reserved {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ValidatePublicURL rejects a stored outbound URL at the trust boundary, before
// it is ever persisted.
//
// The runtime Dialer.Control guard blocks the connection, which is why none of
// these are a breach today. It is not enough on its own, and the OIDC handler's
// own comment says why: accepting https://169.254.169.254 as a stored issuer
// passed a live probe on 2026-06-30. The config is saved, the UI shows a working
// integration, and every delivery fails at dial time with nothing explaining it.
// That guard was then written for OIDC only, so the BYOAI base_url and the
// notification webhook URL, which are the two an ordinary member can set, kept
// accepting anything. One helper, all three surfaces.
//
// A hostname that resolves to a blocked address is deliberately NOT rejected
// here: this runs at config time, resolution can change, and answering "that
// host is internal" for an arbitrary name is itself a disclosure. The dial
// guard owns that decision, on the resolved address, at the moment of use.
func ValidatePublicURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return errors.New("not a valid URL")
	}
	if u.Scheme != "https" {
		return errors.New("must be an https:// URL")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("must include a host")
	}
	if ip := net.ParseIP(host); ip != nil {
		if IsBlockedIP(ip) {
			return errors.New("must not point at a private, loopback or link-local address")
		}
		return nil
	}
	// A bare name with no dot is a container or search-domain name, never a
	// public host, and "localhost" is the obvious one.
	if !strings.Contains(host, ".") || strings.EqualFold(host, "localhost") ||
		strings.HasSuffix(strings.ToLower(host), ".localhost") ||
		strings.HasSuffix(strings.ToLower(host), ".internal") ||
		strings.HasSuffix(strings.ToLower(host), ".local") {
		return errors.New("must be a public hostname")
	}
	return nil
}
