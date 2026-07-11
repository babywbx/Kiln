package security

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

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
