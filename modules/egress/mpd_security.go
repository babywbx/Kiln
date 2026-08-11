package egress

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	packagermpd "github.com/babywbx/kiln/modules/packager/mpd"
	"github.com/babywbx/kiln/modules/pull"
)

const maxFFmpegMPDBytes = 8 << 20

var safeFFmpegRepresentationID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func fetchPinnedMPD(ctx context.Context, opt DashOptions) (string, string, error) {
	if !remoteNetworkSource(opt.SourceURL) {
		return readLocalMPD(opt.SourceURL)
	}
	if opt.Pull == nil {
		return "", "", fmt.Errorf("dash job requires upstream client")
	}
	var lastErr error
	for attempt := 1; attempt <= 6; attempt++ {
		finalURL, body, err := fetchPinnedMPDAttempt(ctx, opt, opt.SourceURL)
		if err == nil {
			return finalURL, body, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		if attempt < 6 {
			select {
			case <-ctx.Done():
				return "", "", ctx.Err()
			case <-time.After(time.Duration(attempt) * 300 * time.Millisecond):
			}
		}
	}
	return "", "", lastErr
}

func fetchPinnedMPDAttempt(ctx context.Context, opt DashOptions, startURL string) (string, string, error) {
	currentURL := startURL
	for redirects := 0; redirects <= 8; redirects++ {
		res, err := opt.Pull.Get(ctx, pull.Request{
			URL:                      currentURL,
			UserAgent:                opt.UserAgent,
			Headers:                  opt.Headers,
			HeaderOrigin:             opt.SourceURL,
			ChannelID:                opt.ChannelID,
			StopRedirect:             true,
			UpgradeInsecureRedirects: opt.UpgradeInsecureRedirects,
		})
		if err != nil {
			return "", "", fmt.Errorf("resolve mpd: %w", err)
		}
		if res.StatusCode >= 300 && res.StatusCode < 400 {
			location := strings.TrimSpace(res.Header.Get("Location"))
			_ = res.Body.Close()
			if location == "" {
				return "", "", fmt.Errorf("mpd redirect missing location")
			}
			base, err := url.Parse(currentURL)
			if err != nil {
				return "", "", err
			}
			reference, err := url.Parse(location)
			if err != nil {
				return "", "", fmt.Errorf("mpd redirect url: %w", err)
			}
			next := base.ResolveReference(reference)
			currentURL = next.String()
			continue
		}
		body, err := io.ReadAll(io.LimitReader(res.Body, maxFFmpegMPDBytes+1))
		_ = res.Body.Close()
		if err != nil {
			return "", "", err
		}
		if len(body) > maxFFmpegMPDBytes {
			return "", "", fmt.Errorf("mpd is too large")
		}
		if !bytes.Contains(body, []byte("<MPD")) && !bytes.Contains(body, []byte("<mpd")) {
			return "", "", fmt.Errorf("resolved url did not return MPD")
		}
		return currentURL, string(body), nil
	}
	return "", "", fmt.Errorf("too many mpd redirects")
}

func validateFFmpegMPD(body, sourceURL string, client *pull.Client, headers map[string]string) error {
	decoder := xml.NewDecoder(strings.NewReader(body))
	decoder.Strict = true
	decoder.Entity = nil
	stack := make([]string, 0, 8)
	rootSeen := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			if !rootSeen {
				return fmt.Errorf("filtered document has no MPD root")
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("parse filtered mpd: %w", err)
		}
		switch value := token.(type) {
		case xml.Directive:
			return fmt.Errorf("filtered mpd directives are not allowed")
		case xml.ProcInst:
			if !strings.EqualFold(value.Target, "xml") {
				return fmt.Errorf("filtered mpd processing instructions are not allowed")
			}
		case xml.StartElement:
			if !rootSeen {
				rootSeen = true
				if value.Name.Local != "MPD" {
					return fmt.Errorf("filtered document root is %q, not MPD", value.Name.Local)
				}
			}
			stack = append(stack, value.Name.Local)
			for _, attribute := range value.Attr {
				name := strings.ToLower(attribute.Name.Local)
				if value.Name.Local == "Representation" && name == "id" && attribute.Value != "" &&
					!safeFFmpegRepresentationID.MatchString(attribute.Value) {
					return fmt.Errorf("unsafe Representation id %q", attribute.Value)
				}
				if name == "href" {
					return fmt.Errorf("external xlink documents are not allowed")
				}
				if mpdURLAttribute(value.Name.Local, name) || genericNetworkReference(attribute, value.Name.Local) {
					if err := validateFFmpegReference(attribute.Value, sourceURL, client, headers); err != nil {
						return err
					}
				}
			}
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 && mpdURLTextElement(stack[len(stack)-1]) {
				if err := validateFFmpegReference(string(value), sourceURL, client, headers); err != nil {
					return err
				}
			}
		}
	}
}

func canUpgradeFFmpegHTTPRedirects(body string) bool {
	decoder := xml.NewDecoder(strings.NewReader(body))
	stack := make([]string, 0, 8)
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return true
		}
		if err != nil {
			return false
		}
		switch value := token.(type) {
		case xml.StartElement:
			stack = append(stack, value.Name.Local)
			for _, attribute := range value.Attr {
				name := strings.ToLower(attribute.Name.Local)
				if (mpdURLAttribute(value.Name.Local, name) || genericNetworkReference(attribute, value.Name.Local)) &&
					explicitHTTPURL(attribute.Value) {
					return false
				}
			}
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 && mpdURLTextElement(stack[len(stack)-1]) && explicitHTTPURL(string(value)) {
				return false
			}
		}
	}
}

func explicitHTTPURL(raw string) bool {
	reference, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && strings.EqualFold(reference.Scheme, "http")
}

func mpdURLAttribute(element, attribute string) bool {
	switch attribute {
	case "media", "index", "initialization", "sourceurl", "serverurl", "url":
		return true
	case "value":
		return strings.EqualFold(element, "UTCTiming")
	default:
		return strings.Contains(attribute, "url") && attribute != "schemeiduri"
	}
}

func mpdURLTextElement(element string) bool {
	switch strings.ToLower(element) {
	case "baseurl", "location", "patchlocation":
		return true
	default:
		return false
	}
}

func genericNetworkReference(attribute xml.Attr, element string) bool {
	name := strings.ToLower(attribute.Name.Local)
	if name == "xmlns" || attribute.Name.Space == "xmlns" || name == "id" || name == "value" || name == "schemeiduri" ||
		strings.EqualFold(element, "MPD") && name == "profiles" {
		return false
	}
	return looksLikeAbsoluteReference(attribute.Value)
}

func looksLikeAbsoluteReference(raw string) bool {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, `\\`) {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme != ""
}

func validateFFmpegReference(raw, sourceURL string, client *pull.Client, headers map[string]string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.Contains(raw, `\`) {
		return fmt.Errorf("ffmpeg mpd contains an unsafe network path")
	}
	reference, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("ffmpeg mpd url is invalid: %w", err)
	}
	if reference.Scheme == "" && reference.Host == "" {
		return nil
	}
	base, err := url.Parse(sourceURL)
	if err != nil {
		return err
	}
	absolute := base.ResolveReference(reference)
	switch strings.ToLower(absolute.Scheme) {
	case "http", "https":
		if absolute.Hostname() == "" || client == nil {
			return fmt.Errorf("ffmpeg mpd url requires a guarded network client")
		}
		if hasFFmpegCustomHeaders(headers) && !sameURLOrigin(absolute.String(), sourceURL) {
			return fmt.Errorf("ffmpeg mpd host crosses the authorized header origin")
		}
	case "file":
		if remoteNetworkSource(sourceURL) || absolute.Host != "" {
			return fmt.Errorf("ffmpeg mpd file url is not allowed")
		}
	default:
		return fmt.Errorf("ffmpeg mpd url scheme %q is not allowed", absolute.Scheme)
	}
	return nil
}

func ffmpegMPDRefreshInterval(body string) time.Duration {
	var root struct {
		Type          string `xml:"type,attr"`
		MinimumUpdate string `xml:"minimumUpdatePeriod,attr"`
	}
	if xml.Unmarshal([]byte(body), &root) != nil || !strings.EqualFold(root.Type, "dynamic") {
		return 0
	}
	interval := 2 * time.Second
	if parsed, err := packagermpd.ParseDuration(root.MinimumUpdate); err == nil && parsed > 0 {
		interval = parsed
	}
	return min(max(interval, 500*time.Millisecond), 30*time.Second)
}

func writeFFmpegMPD(path string, body []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".input-*.mpd")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, err = temporary.Write(body); err == nil {
		err = temporary.Close()
	} else {
		_ = temporary.Close()
	}
	if err != nil {
		return err
	}
	return replaceFFmpegMPD(temporaryPath, path)
}

func sameURLOrigin(left, right string) bool {
	a, err := url.Parse(left)
	if err != nil || a.Hostname() == "" {
		return false
	}
	b, err := url.Parse(right)
	if err != nil || b.Hostname() == "" {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(strings.TrimSuffix(a.Hostname(), "."), strings.TrimSuffix(b.Hostname(), ".")) &&
		mpdOriginPort(a) == mpdOriginPort(b)
}

func mpdOriginPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if strings.EqualFold(u.Scheme, "http") {
		return "80"
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return ""
}

func copyProxyRequestHeaders(source http.Header, custom map[string]string) http.Header {
	out := make(http.Header)
	for _, name := range []string{"Accept", "Accept-Encoding", "If-Modified-Since", "If-None-Match", "If-Range", "Range"} {
		for _, value := range source.Values(name) {
			out.Add(name, value)
		}
	}
	for name := range custom {
		out.Del(name)
	}
	return out
}
