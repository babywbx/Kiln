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
	if !explicitlyAllowed && (host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".internal") || strings.Contains(host, "metadata")) {
		return nil, fmt.Errorf("probe target is not public")
	}
	var selected net.IP
	if ip := net.ParseIP(host); ip != nil {
		if !explicitlyAllowed && !publicProbeIP(ip) {
			return nil, fmt.Errorf("probe target is not public")
		}
		selected = ip
	} else {
		addresses, lookupErr := net.DefaultResolver.LookupIPAddr(ctx, host)
		if lookupErr != nil || len(addresses) == 0 {
			return nil, fmt.Errorf("probe target dns lookup failed")
		}
		for _, address := range addresses {
			if !explicitlyAllowed && !publicProbeIP(address.IP) {
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

func publicProbeIP(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsUnspecified() &&
		!ip.IsMulticast() && !carrierGradeNAT.Contains(ip)
}
