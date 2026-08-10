package node

import (
	"context"
	"fmt"
)

func (c *Client) browserControl(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	if c.browser == nil {
		return nil, ErrBrowserUnavailable
	}
	safeParams, err := sanitizeBrowserParams(action, params)
	if err != nil {
		return nil, err
	}
	if action != "screenshot" {
		return c.browser.Execute(ctx, action, safeParams)
	}
	return c.browser.ExecuteScreenshot(ctx, safeParams, func(path, logicalName, contentType string) (map[string]any, error) {
		return c.publishPresentationFile(ctx, path, logicalName, contentType)
	})
}

func sanitizeBrowserParams(action string, params map[string]any) (map[string]any, error) {
	allowed := map[string]struct{}{}
	add := func(keys ...string) {
		for _, key := range keys {
			allowed[key] = struct{}{}
		}
	}
	page := []string{"browserSessionId", "pageId", "timeoutMs"}
	switch action {
	case "launch":
		add("engine", "headless", "viewport")
	case "close", "pages.list":
		add("browserSessionId")
	case "page.open":
		add("browserSessionId", "url", "waitUntil", "timeoutMs")
	case "page.navigate":
		add(page...)
		add("url", "waitUntil")
	case "page.close", "snapshot":
		add(page...)
	case "click":
		add(page...)
		add("locator")
	case "type":
		add(page...)
		add("locator", "text")
	case "press":
		add(page...)
		add("locator", "key")
	case "wait":
		add(page...)
		add("locator", "state")
	case "screenshot":
		add(page...)
		add("fullPage", "format", "quality")
	case "events":
		add("browserSessionId", "cursor")
	default:
		return nil, &BrowserActionError{Code: "INVALID_REQUEST", Message: "unsupported browser action", Retryable: false}
	}
	out := make(map[string]any, len(params))
	for key, value := range params {
		if _, ok := allowed[key]; !ok {
			return nil, &BrowserActionError{Code: "INVALID_REQUEST", Message: fmt.Sprintf("browser parameter %q is not allowed for %s", key, action), Retryable: false}
		}
		out[key] = value
	}
	return out, nil
}
