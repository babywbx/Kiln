package security

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
)

var carrierGradeNAT = mustCIDR("100.64.0.0/10")

func mustCIDR(raw string) *net.IPNet {
	_, network, err := net.ParseCIDR(raw)
	if err != nil {
		panic(err)
	}
	return network
}

func HostAllowed(rawURL string, allowed map[string]struct{}) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme not allowed")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("empty host")
	}
	if _, ok := allowed[host]; !ok {
		return fmt.Errorf("host not allowed: %s", host)
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return nil
		}
	}
	return nil
}

func MediaHostOK(rawURL string, allowedPrivate map[string]struct{}) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme not allowed")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return fmt.Errorf("empty host")
	}
	_, explicitlyAllowed := allowedPrivate[host]
	if metadataHost(host) {
		return fmt.Errorf("host not allowed: %s", host)
	}
	if !explicitlyAllowed && (host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".internal")) {
		return fmt.Errorf("host not allowed: %s", host)
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() || carrierGradeNAT.Contains(ip) {
			return fmt.Errorf("host not allowed: %s", host)
		}
		if ip.String() == "169.254.169.254" {
			return fmt.Errorf("host not allowed: %s", host)
		}
		if ip.IsLoopback() || ip.IsPrivate() {
			if !explicitlyAllowed {
				return fmt.Errorf("private host not allowlisted: %s", host)
			}
		}
		return nil
	}
	if explicitlyAllowed {
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
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, fmt.Errorf("invalid public probe url")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	_, explicitlyAllowed := allowedPrivate[host]
	if metadataHost(host) {
		return nil, fmt.Errorf("probe target is not public")
	}
	if !explicitlyAllowed && (host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".internal")) {
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

func probeIPAllowed(ip net.IP, explicitlyAllowed bool) bool {
	if ip == nil || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() ||
		ip.IsMulticast() || carrierGradeNAT.Contains(ip) {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() {
		return explicitlyAllowed
	}
	return ip.IsGlobalUnicast()
}

func IsPublicIP(ip net.IP) bool {
	return probeIPAllowed(ip, false)
}

func metadataHost(host string) bool {
	return host == "metadata.google.internal" ||
		strings.HasSuffix(host, ".internal") && slices.Contains(strings.Split(host, "."), "metadata")
}
