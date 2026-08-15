package security

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strings"
)

var fixedSpecialUsePrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.31.196.0/24"),
	netip.MustParsePrefix("192.52.193.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.175.48.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("100:0:0:1::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2620:4f:8000::/48"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
}

func MediaHostOK(rawURL string, allowedPrivate map[string]struct{}) error {
	_, host, err := parseMediaURL(rawURL)
	if err != nil {
		return err
	}
	_, explicitlyAllowed := allowedPrivate[host]
	if !mediaHostnameAllowed(host, explicitlyAllowed) {
		return fmt.Errorf("host not allowed: %s", host)
	}
	if ip := net.ParseIP(host); ip != nil {
		address, ok := canonicalIP(ip)
		if !ok || fixedDestination(address) {
			return fmt.Errorf("host not allowed: %s", host)
		}
		if address.IsLoopback() || address.IsPrivate() {
			if !explicitlyAllowed {
				return fmt.Errorf("private host not allowlisted: %s", host)
			}
		}
		return nil
	}
	return nil
}

func PublicProbeURL(ctx context.Context, rawURL string, allowedPrivate map[string]struct{}) error {
	_, err := PinPublicProbeURL(ctx, rawURL, allowedPrivate)
	return err
}

// Host is pinned to an IP; callers must keep the original Host header.
func PinPublicProbeURL(ctx context.Context, rawURL string, allowedPrivate map[string]struct{}) (*url.URL, error) {
	u, host, err := parseMediaURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid public probe url")
	}
	_, explicitlyAllowed := allowedPrivate[host]
	if !mediaHostnameAllowed(host, explicitlyAllowed) {
		return nil, fmt.Errorf("probe target is not public")
	}
	var selected net.IP
	if ip := net.ParseIP(host); ip != nil {
		if !probeIPAllowed(ip, explicitlyAllowed) {
			return nil, fmt.Errorf("probe target is not public")
		}
		selected = ip
	} else {
		addresses, lookupErr := net.DefaultResolver.LookupIPAddr(ctx, host)
		if lookupErr != nil {
			return nil, fmt.Errorf("probe target dns lookup failed: %w", lookupErr)
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("probe target dns lookup failed")
		}
		for _, address := range addresses {
			if !probeIPAllowed(address.IP, explicitlyAllowed) {
				return nil, fmt.Errorf("probe target resolved to a non-public address")
			}
			if selected == nil {
				selected = address.IP
			}
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("probe target dns lookup failed")
	}
	pinned := *u
	port := u.Port()
	if port != "" {
		pinned.Host = net.JoinHostPort(selected.String(), port)
	} else if selected.To4() == nil {
		pinned.Host = "[" + selected.String() + "]"
	} else {
		pinned.Host = selected.String()
	}
	return &pinned, nil
}

func parseMediaURL(rawURL string) (*url.URL, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("invalid url")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, "", fmt.Errorf("scheme not allowed")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return nil, "", fmt.Errorf("empty host")
	}
	return u, host, nil
}

func mediaHostnameAllowed(host string, explicitlyAllowed bool) bool {
	if metadataHost(host) {
		return false
	}
	return explicitlyAllowed || host != "localhost" && !strings.HasSuffix(host, ".localhost") &&
		!strings.HasSuffix(host, ".local") && !strings.HasSuffix(host, ".internal")
}

func probeIPAllowed(ip net.IP, explicitlyAllowed bool) bool {
	address, ok := canonicalIP(ip)
	if !ok || fixedDestination(address) {
		return false
	}
	if address.IsLoopback() || address.IsPrivate() {
		return explicitlyAllowed
	}
	return address.IsGlobalUnicast()
}

func IsPublicIP(ip net.IP) bool {
	return probeIPAllowed(ip, false)
}

func canonicalIP(ip net.IP) (netip.Addr, bool) {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Addr{}, false
	}
	return address.Unmap(), true
}

func fixedDestination(ip netip.Addr) bool {
	if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
		return true
	}
	for _, prefix := range fixedSpecialUsePrefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func metadataHost(host string) bool {
	return host == "metadata.google.internal" ||
		strings.HasSuffix(host, ".internal") && slices.Contains(strings.Split(host, "."), "metadata")
}
