package security

import (
	"net/http/httptest"
	"testing"
)

func TestClientIPRemovesTheRemotePort(t *testing.T) {
	request := httptest.NewRequest("GET", "http://kiln.test/", nil)
	request.RemoteAddr = "192.0.2.10:43210"
	if got := ClientIP(request); got != "192.0.2.10" {
		t.Fatalf("ClientIP() = %q", got)
	}
}

func TestLocalHealthRequestRequiresLoopbackAndAnExactHealthPath(t *testing.T) {
	request := httptest.NewRequest("GET", "http://kiln.test/healthz", nil)
	request.RemoteAddr = "127.0.0.1:43210"
	if !IsLocalHealthRequest(request) {
		t.Fatal("loopback health request was not recognized")
	}
	request.URL.Path = "/v1/play/news/index.m3u8"
	if IsLocalHealthRequest(request) {
		t.Fatal("non-health request bypassed host validation")
	}
	request.URL.Path = "/readyz"
	request.RemoteAddr = "192.0.2.10:43210"
	if IsLocalHealthRequest(request) {
		t.Fatal("remote readiness request bypassed host validation")
	}
}

func TestRequestHostAllowedHandlesPortsAndIPv6(t *testing.T) {
	tests := []struct {
		host    string
		allowed []string
		want    bool
	}{
		{host: "kiln.example:8080", allowed: []string{"kiln.example"}, want: true},
		{host: "[2001:db8::1]:8080", allowed: []string{"2001:db8::1"}, want: true},
		{host: "other.example", allowed: []string{"kiln.example"}, want: false},
		{host: "anything.example", allowed: []string{"*"}, want: true},
	}
	for _, test := range tests {
		request := httptest.NewRequest("GET", "http://kiln.test/", nil)
		request.Host = test.host
		if got := RequestHostAllowed(request, test.allowed); got != test.want {
			t.Fatalf("RequestHostAllowed(%q, %v) = %v, want %v", test.host, test.allowed, got, test.want)
		}
	}
}
