// Package netguard centralizes the SSRF destination-blocking rule so every outbound
// tenant-influenced request (BYOAI base_url, alert webhooks, OIDC discovery) applies the
// same list and it cannot drift between call sites.
package netguard

import "net"

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
