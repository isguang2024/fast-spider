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
		return c.publishScreenshotPresentation(ctx, path, logicalName, contentType)
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
	case "readiness":
		// Readiness is a process-local, read-only probe and accepts no session
		// identifiers or browser action parameters.
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
		add("ref", "locator")
	case "type":
		add(page...)
		add("ref", "locator", "text")
	case "press":
		add(page...)
		add("ref", "locator", "key")
	case "wait":
		add(page...)
		add("ref", "locator", "state")
	case "batch":
		add(page...)
		add("steps", "snapshotAfter")
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
	if action == "batch" {
		steps, err := sanitizeBrowserBatchSteps(out["steps"])
		if err != nil {
			return nil, err
		}
		out["steps"] = steps
	}
	if action == "page.open" || action == "page.navigate" {
		rawURL, ok := out["url"].(string)
		if !ok {
			return nil, &BrowserActionError{Code: "BROWSER_NETWORK_DENIED", Message: "navigation URL is invalid", Retryable: false}
		}
		if err := validateBrowserNavigationURL(rawURL); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func sanitizeBrowserBatchSteps(value any) ([]any, error) {
	var raw []any
	switch typed := value.(type) {
	case []any:
		raw = typed
	case []map[string]any:
		raw = make([]any, 0, len(typed))
		for _, step := range typed {
			raw = append(raw, step)
		}
	default:
		return nil, &BrowserActionError{Code: "INVALID_REQUEST", Message: "batch steps must be an array", Retryable: false}
	}
	if len(raw) < 1 || len(raw) > 32 {
		return nil, &BrowserActionError{Code: "INVALID_REQUEST", Message: "batch steps must contain 1-32 actions", Retryable: false}
	}
	out := make([]any, 0, len(raw))
	for index, item := range raw {
		step, ok := item.(map[string]any)
		if !ok {
			return nil, &BrowserActionError{Code: "INVALID_REQUEST", Message: fmt.Sprintf("batch step %d is invalid", index+1), Retryable: false}
		}
		action, _ := step["action"].(string)
		allowed := map[string]struct{}{"action": {}, "ref": {}, "locator": {}, "timeoutMs": {}}
		switch action {
		case "click":
		case "type":
			allowed["text"] = struct{}{}
		case "press":
			allowed["key"] = struct{}{}
		case "wait":
			allowed["state"] = struct{}{}
		default:
			return nil, &BrowserActionError{Code: "INVALID_REQUEST", Message: fmt.Sprintf("batch step %d action is not allowed", index+1), Retryable: false}
		}
		clean := make(map[string]any, len(step))
		for key, itemValue := range step {
			if _, ok := allowed[key]; !ok {
				return nil, &BrowserActionError{Code: "INVALID_REQUEST", Message: fmt.Sprintf("batch step %d parameter %q is not allowed", index+1, key), Retryable: false}
			}
			clean[key] = itemValue
		}
		out = append(out, clean)
	}
	return out, nil
}
