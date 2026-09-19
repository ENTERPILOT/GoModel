package auditlog

import (
	"net"
	"net/http"
	"slices"
	"strings"
)

// TrustedProxies resolves the client address of a request that arrived through
// operator-owned proxies. A nil *TrustedProxies means the gateway faces clients
// directly, so forwarding headers are never consulted.
type TrustedProxies struct {
	nets []*net.IPNet
}

// ParseTrustedProxies compiles CIDR networks for client IP resolution. Entries
// that are neither a valid address nor a valid network are skipped: the
// configuration loader rejects them with a startup error, so a value reaching
// here is either usable or already reported.
func ParseTrustedProxies(cidrs []string) *TrustedProxies {
	var nets []*net.IPNet
	for _, raw := range cidrs {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if _, ipNet, err := net.ParseCIDR(value); err == nil {
			nets = append(nets, ipNet)
			continue
		}
		if ip := net.ParseIP(value); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		}
	}
	if len(nets) == 0 {
		return nil
	}
	return &TrustedProxies{nets: nets}
}

// Contains reports whether an address belongs to a trusted proxy network.
func (t *TrustedProxies) Contains(ip net.IP) bool {
	if t == nil || ip == nil {
		return false
	}
	for _, ipNet := range t.nets {
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP returns the address to tag an audit entry with, given the address
// resolved by the HTTP server (directIP). Forwarded-for headers are consulted
// only when the request arrived from a trusted proxy; the nearest hop that is
// not itself a trusted proxy is reported, because that is the last address the
// operator's own infrastructure wrote and the furthest one a client cannot
// overwrite. Requests with no usable header, or with a header GoModel cannot
// trust, keep directIP.
func (t *TrustedProxies) ClientIP(req *http.Request, directIP string) string {
	if t == nil || req == nil {
		return directIP
	}
	if !t.Contains(socketIP(req)) {
		return directIP
	}
	values := req.Header.Values("X-Forwarded-For")
	if len(values) == 0 {
		return directIP
	}
	var hops []net.IP
	for _, value := range values {
		for candidate := range strings.SplitSeq(value, ",") {
			ip := net.ParseIP(trimIPBrackets(strings.TrimSpace(candidate)))
			if ip == nil {
				// A chain GoModel cannot fully read is not evidence about the
				// client, so keep the address of the socket peer instead.
				return directIP
			}
			hops = append(hops, ip)
		}
	}
	// Reading from the hop nearest the gateway inward finds the last address
	// the operator's own infrastructure wrote, which a client cannot overwrite.
	for _, hop := range slices.Backward(hops) {
		if !t.Contains(hop) {
			return hop.String()
		}
	}
	// Every hop is a trusted proxy, so no hop was written by anything the
	// gateway does not already trust: the leftmost address is a claim, not
	// evidence, and an internal client could invent it. Record the address of
	// the socket peer instead of attributing the request to a forgeable value.
	return directIP
}

// socketIP is the peer address of the connection, independent of any header.
// A link-local IPv6 peer names its zone ("fe80::1%eth0"); the zone identifies
// an interface rather than part of the address, and net.ParseIP rejects it, so
// it is dropped before parsing to keep a zone-bearing proxy recognizable.
func socketIP(req *http.Request) net.IP {
	remote := req.RemoteAddr
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	if zone := strings.LastIndexByte(host, '%'); zone >= 0 {
		host = host[:zone]
	}
	return net.ParseIP(host)
}

// trimIPBrackets removes the brackets a proxy may put around an IPv6 address
// inside a forwarded-for list, so "2001:db8::1, [2001:db8::2]" reads as two
// addresses rather than one unparseable value.
func trimIPBrackets(value string) string {
	return strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
}
