package providers

import (
	"errors"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// bareBaseURLPattern matches a base URL written without a scheme, which the
// Bedrock adapters read as an AWS region ("us-east-1"). No adapter can send a
// request to any other scheme-less value.
var bareBaseURLPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)+$`)

// metadataPrefixes are the address ranges instance metadata services answer
// on: IPv4 and IPv6 link-local, and the fixed addresses cloud providers use
// outside those. Loopback and private ranges are deliberately absent: local
// and self-hosted model servers live there.
var metadataPrefixes = []netip.Prefix{
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("fd00:ec2::/32"),
	netip.MustParsePrefix("100.100.100.200/32"),
}

// metadataHosts are the names instance metadata services answer on.
var metadataHosts = []string{
	"metadata",
	"metadata.google.internal",
	"metadata.goog",
	"instance-data",
	"instance-data.ec2.internal",
}

// validateCredentialBaseURL checks where a saved credential points before
// anything is sent there. Requests are built by appending a path to the base
// URL, so it has to be a plain http(s) origin with an optional path: a query
// or fragment would swallow the appended path, and userinfo would put
// credentials in a field that is shown unmasked. Instance metadata endpoints
// are never a model provider. The check reads the URL as written; it does not
// resolve host names.
func validateCredentialBaseURL(cred ManagedProviderCredential) error {
	raw := strings.TrimSpace(cred.BaseURL)
	if raw == "" {
		return nil
	}
	if err := checkBaseURL(raw); err != nil {
		return credentialFieldError(CredentialFieldBaseURL, "%s", err.Error())
	}
	return nil
}

func checkBaseURL(raw string) error {
	if !strings.Contains(raw, "://") {
		if bareBaseURLPattern.MatchString(raw) {
			return nil
		}
		return errors.New("base URL must start with http:// or https://")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("base URL is not a valid URL")
	}
	if scheme := strings.ToLower(u.Scheme); scheme != "http" && scheme != "https" {
		return errors.New("base URL must start with http:// or https://")
	}
	if u.User != nil {
		return errors.New("base URL must not include a username or password")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.HasSuffix(raw, "#") {
		return errors.New("base URL must not include a query or fragment")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return errors.New("base URL must include a host")
	}
	if slices.Contains(metadataHosts, host) {
		return errors.New("base URL must not point at an instance metadata endpoint")
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		// A name whose last label is a number is an IP address in a legacy
		// spelling (decimal, hex, short forms) that system resolvers accept;
		// only the canonical form is taken, so the range check below sees it.
		if numericLabel(host[strings.LastIndexByte(host, '.')+1:]) {
			return errors.New("base URL host must be a host name or a standard IP address")
		}
		return nil
	}
	addr = addr.Unmap().WithZone("")
	for _, prefix := range metadataPrefixes {
		if prefix.Contains(addr) {
			return errors.New("base URL must not point at a link-local or instance metadata address")
		}
	}
	return nil
}

// numericLabel reports whether a host label is a decimal or 0x-prefixed
// hexadecimal number.
func numericLabel(label string) bool {
	digits := "0123456789"
	if rest, ok := strings.CutPrefix(label, "0x"); ok {
		label, digits = "0"+rest, "0123456789abcdef"
	}
	return label != "" && strings.Trim(label, digits) == ""
}
