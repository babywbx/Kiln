package security

import (
	"context"
	"fmt"
	"net"
	"net/url"
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
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("empty host")
	}
	if host == "metadata.google.internal" || strings.HasSuffix(host, ".internal") && strings.Contains(host, "metadata") {
		return fmt.Errorf("host not allowed: %s", host)
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
			return fmt.Errorf("host not allowed: %s", host)
		}
		if ip.String() == "169.254.169.254" {
			return fmt.Errorf("host not allowed: %s", host)
		}
		if ip.IsLoopback() || ip.IsPrivate() {
			if _, ok := allowedPrivate[host]; !ok {
				return fmt.Errorf("private host not allowlisted: %s", host)
			}
		}
		return nil
	}
	if _, ok := allowedPrivate[host]; ok {
		return nil
	}
	return nil
}

// PublicProbeURL rejects private and special-use destinations before a custom
// connectivity probe is sent. Fixed public probe presets do not accept a URL
// from the client and therefore do not need this path.
func PublicProbeURL(ctx context.Context, rawURL string, allowedPrivate map[string]struct{}) error {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return fmt.Errorf("invalid public probe url")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if _, ok := allowedPrivate[host]; ok {
		return nil
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".internal") || strings.Contains(host, "metadata") {
		return fmt.Errorf("probe target is not public")
	}
	if ip := net.ParseIP(host); ip != nil {
		if !publicProbeIP(ip) {
			return fmt.Errorf("probe target is not public")
		}
		return nil
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return fmt.Errorf("probe target dns lookup failed")
	}
	for _, address := range addresses {
		if !publicProbeIP(address.IP) {
			return fmt.Errorf("probe target resolved to a non-public address")
		}
	}
	return nil
}

func publicProbeIP(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsUnspecified() &&
		!ip.IsMulticast() && !carrierGradeNAT.Contains(ip)
}
