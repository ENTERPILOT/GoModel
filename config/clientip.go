package config

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// ClientIPHeaderForwardedFor is the default header GoModel reads the client
// address from. It carries a chain of hops rather than a single address, so it
// is the only header resolved by walking the chain.
const ClientIPHeaderForwardedFor = "X-Forwarded-For"

// clientIPPresets expand a shorthand name into the networks it stands for.
// They are opt-in: nothing here is trusted unless it is named in the
// configuration, so a gateway that faces clients directly keeps ignoring every
// forwarding header.
var clientIPPresets = map[string][]string{
	// loopback covers a proxy sharing the host with the gateway.
	"loopback": {"127.0.0.0/8", "::1/128"},
	// private covers RFC 1918 and RFC 4193 (ULA) space, the usual home for a
	// container network or a cluster ingress whose address is not fixed.
	// Prefer listing the one subnet your proxy sits on when you know it: this
	// preset trusts every private client to set its own forwarded address.
	"private": {"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"},
}

// ClientIPPolicy is the resolved form of the server's client-address settings:
// which peers may speak about the client, which header they speak through, and
// how to read it. The zero value trusts nothing, which makes every request
// report the address of the socket that reached the gateway.
type ClientIPPolicy struct {
	// Prefixes are the networks whose requests may carry a client address.
	Prefixes []netip.Prefix
	// Header carries the client address. Empty when no proxy is trusted.
	Header string
	// Hops is the number of proxies between the client and the gateway. Zero
	// selects chain-scanning by Prefixes instead of a fixed depth.
	Hops int
	// chain reports whether Header carries a comma-separated list of hops
	// rather than a single address.
	chain bool
}

// Enabled reports whether any peer is trusted to speak about the client.
func (p ClientIPPolicy) Enabled() bool { return len(p.Prefixes) > 0 }

// Trusts reports whether an address belongs to a configured proxy network.
func (p ClientIPPolicy) Trusts(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	addr = canonicalAddr(addr)
	for _, prefix := range p.Prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// Resolve returns the address to attribute a request to. Forwarding headers
// are read only when the request arrived from a trusted network; anything the
// policy cannot vouch for falls back to the address of the socket peer, which
// no client can forge.
func (p ClientIPPolicy) Resolve(req *http.Request) string {
	direct := socketAddr(req)
	if !p.Enabled() || !p.Trusts(direct) {
		return addrString(direct, req)
	}
	if !p.chain {
		return p.singleHeaderIP(req, direct)
	}
	return p.chainIP(req, direct)
}

// singleHeaderIP reads a header that carries exactly one address, such as
// CF-Connecting-IP or X-Real-IP. Its value is taken verbatim: the edge that
// wrote it already decided who the client is, and a chain scan would be the
// wrong question to ask of it.
func (p ClientIPPolicy) singleHeaderIP(req *http.Request, direct netip.Addr) string {
	addr, err := netip.ParseAddr(trimIPBrackets(strings.TrimSpace(req.Header.Get(p.Header))))
	if err != nil {
		return addrString(direct, req)
	}
	return canonicalAddr(addr).String()
}

// chainIP reads X-Forwarded-For, where each hop appends the address it saw.
//
// With Hops set, the client sits exactly that many proxies out, so the entry
// at that depth is the answer regardless of the addresses in between — which
// is what you want when your edge rotates through addresses you cannot
// enumerate. Otherwise the nearest hop that is not itself a trusted proxy is
// reported: that is the last address the operator's own infrastructure wrote
// and the furthest one a client cannot overwrite.
func (p ClientIPPolicy) chainIP(req *http.Request, direct netip.Addr) string {
	values := req.Header.Values(p.Header)
	if len(values) == 0 {
		return addrString(direct, req)
	}
	var hops []netip.Addr
	for _, value := range values {
		for candidate := range strings.SplitSeq(value, ",") {
			addr, err := netip.ParseAddr(trimIPBrackets(strings.TrimSpace(candidate)))
			if err != nil {
				// A chain GoModel cannot fully read is not evidence about the
				// client, so keep the address of the socket peer instead.
				return addrString(direct, req)
			}
			hops = append(hops, canonicalAddr(addr))
		}
	}
	if p.Hops > 0 {
		index := len(hops) - p.Hops
		if index < 0 {
			// A chain shorter than the configured depth did not come through
			// the expected path, so it says nothing about the client.
			return addrString(direct, req)
		}
		return hops[index].String()
	}
	for _, hop := range slices.Backward(hops) {
		if !p.Trusts(hop) {
			return hop.String()
		}
	}
	// Every hop is a trusted proxy, so no hop was written by anything the
	// gateway does not already trust: the leftmost address is a claim, not
	// evidence, and an internal client could invent it. Record the address of
	// the socket peer instead of attributing the request to a forgeable value.
	return addrString(direct, req)
}

// ResolveClientIPPolicy validates the server's client-address settings and
// compiles them into ServerConfig.ClientIP, rewriting TrustedProxies as the
// networks it resolved so logs and config dumps show what is actually trusted.
func ResolveClientIPPolicy(cfg *ServerConfig) error {
	if cfg == nil {
		return nil
	}
	prefixes, err := parseTrustedProxies(cfg.TrustedProxies)
	if err != nil {
		return err
	}
	header, chain, err := clientIPHeader(cfg.ClientIPHeader)
	if err != nil {
		return err
	}
	if cfg.TrustedHops < 0 {
		return fmt.Errorf("server.trusted_hops must be 0 or a positive number of proxies; got %d", cfg.TrustedHops)
	}
	if cfg.TrustedHops > 0 && !chain {
		return fmt.Errorf("server.trusted_hops counts hops in a %s chain and cannot be used with server.client_ip_header %q, which carries a single address", ClientIPHeaderForwardedFor, header)
	}
	if len(prefixes) == 0 {
		if strings.TrimSpace(cfg.ClientIPHeader) != "" {
			return fmt.Errorf("server.client_ip_header is set but server.trusted_proxies is empty; list the networks your proxies sit on or the header is ignored")
		}
		if cfg.TrustedHops > 0 {
			return fmt.Errorf("server.trusted_hops is set but server.trusted_proxies is empty; list the networks your proxies sit on or the header is ignored")
		}
		cfg.TrustedProxies = nil
		cfg.ClientIP = ClientIPPolicy{}
		return nil
	}

	normalized := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		normalized = append(normalized, prefix.String())
	}
	cfg.TrustedProxies = normalized
	cfg.ClientIP = ClientIPPolicy{
		Prefixes: prefixes,
		Header:   header,
		Hops:     cfg.TrustedHops,
		chain:    chain,
	}
	return nil
}

// clientIPHeader canonicalizes the configured header and reports whether it
// carries a chain of hops. RFC 7239 Forwarded is rejected rather than read as
// a single address: its value is a list of key-value pairs, so treating it as
// an address would silently resolve nothing.
func clientIPHeader(value string) (string, bool, error) {
	header, err := NormalizeHeaderName(value, ClientIPHeaderForwardedFor)
	if err != nil {
		return "", false, fmt.Errorf("server.client_ip_header: %w", err)
	}
	if strings.EqualFold(header, "Forwarded") {
		return "", false, fmt.Errorf("server.client_ip_header: the RFC 7239 %q header is not supported; use %s or a single-address header such as X-Real-IP or CF-Connecting-IP", header, ClientIPHeaderForwardedFor)
	}
	return header, strings.EqualFold(header, ClientIPHeaderForwardedFor), nil
}

// parseTrustedProxies expands presets, accepts bare addresses as single-host
// networks, drops duplicates and blanks, and rejects anything that is neither.
func parseTrustedProxies(values []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(values))
	seen := make(map[netip.Prefix]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		expanded, isPreset := clientIPPresets[strings.ToLower(value)]
		if !isPreset {
			expanded = []string{value}
		}
		for _, entry := range expanded {
			prefix, err := parseTrustedProxy(entry, raw)
			if err != nil {
				return nil, err
			}
			if _, duplicate := seen[prefix]; duplicate {
				continue
			}
			seen[prefix] = struct{}{}
			prefixes = append(prefixes, prefix)
		}
	}
	if len(prefixes) == 0 {
		return nil, nil
	}
	return prefixes, nil
}

// parseTrustedProxy reads one network or bare address. An IPv4-mapped network
// is rewritten as the IPv4 network it stands for, so "::ffff:10.0.0.0/104"
// matches the same requests as "10.0.0.0/8"; one whose prefix reaches past the
// embedded address matches no IPv4 request at all and is rejected instead of
// silently trusting nothing.
func parseTrustedProxy(entry, raw string) (netip.Prefix, error) {
	if prefix, err := netip.ParsePrefix(entry); err == nil {
		addr := prefix.Addr()
		if addr.Is4In6() {
			if prefix.Bits() < 96 {
				return netip.Prefix{}, fmt.Errorf("server.trusted_proxies: %q is an IPv4-mapped network with a prefix shorter than /96, which matches no IPv4 address; write the IPv4 form (for example 10.0.0.0/8)", entry)
			}
			prefix = netip.PrefixFrom(addr.Unmap(), prefix.Bits()-96)
		}
		return prefix.Masked(), nil
	}
	if addr, err := netip.ParseAddr(entry); err == nil {
		addr = canonicalAddr(addr)
		return netip.PrefixFrom(addr, addr.BitLen()), nil
	}
	return netip.Prefix{}, fmt.Errorf("server.trusted_proxies: %q is not a valid IP address, CIDR network, or preset (%s)", raw, strings.Join(clientIPPresetNames(), ", "))
}

// clientIPPresetNames lists the preset names in a stable order for messages.
func clientIPPresetNames() []string {
	names := make([]string, 0, len(clientIPPresets))
	for name := range clientIPPresets {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// canonicalAddr reduces an address to the form networks are compared in: an
// IPv4-mapped address becomes its IPv4 form, and the zone of a link-local
// address is dropped because it names an interface rather than the peer.
func canonicalAddr(addr netip.Addr) netip.Addr {
	return addr.Unmap().WithZone("")
}

// socketAddr is the peer address of the connection, independent of any header.
func socketAddr(req *http.Request) netip.Addr {
	if req == nil {
		return netip.Addr{}
	}
	remote := req.RemoteAddr
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	addr, err := netip.ParseAddr(trimIPBrackets(remote))
	if err != nil {
		return netip.Addr{}
	}
	return canonicalAddr(addr)
}

// addrString renders the socket peer, falling back to the raw RemoteAddr when
// it is not an address at all (a unix socket, say) so the caller still gets
// whatever the server knows about the peer.
func addrString(addr netip.Addr, req *http.Request) string {
	if addr.IsValid() {
		return addr.String()
	}
	if req == nil {
		return ""
	}
	if host, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
		return host
	}
	return req.RemoteAddr
}

// trimIPBrackets removes the brackets a proxy may put around an IPv6 address
// inside a forwarded-for list, so "2001:db8::1, [2001:db8::2]" reads as two
// addresses rather than one unparseable value.
func trimIPBrackets(value string) string {
	return strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
}
