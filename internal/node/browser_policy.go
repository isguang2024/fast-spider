package node

import (
	"net/url"
	"strings"
)

// validateBrowserNavigationURL enforces only the local Browser product's
// fundamental URL boundary. Network reachability and address classes are
// deliberately left to Chromium and the Node's operating system.
func validateBrowserNavigationURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || parsed.Opaque != "" || parsed.Host == "" {
		return &BrowserActionError{Code: "BROWSER_NETWORK_DENIED", Message: "navigation URL is invalid", Retryable: false}
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return &BrowserActionError{Code: "BROWSER_NETWORK_DENIED", Message: "navigation scheme is not allowed", Retryable: false}
	}
	if parsed.User != nil {
		return &BrowserActionError{Code: "BROWSER_NETWORK_DENIED", Message: "navigation URL must not contain credentials", Retryable: false}
	}
	return nil
}
