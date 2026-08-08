package node

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxBrowserOrigins = 32

var (
	ErrBrowserOriginDenied = errors.New("browser origin is not authorized")
	ErrBrowserDNSChanged   = errors.New("browser origin DNS no longer matches pinned addresses")
	ErrBrowserOriginUnsafe = errors.New("browser origin resolves to an unsafe address")
)

type BrowserOriginRecord struct {
	Origin    string   `json:"origin"`
	PinnedIPs []string `json:"pinnedIps"`
}

type BrowserOriginSummary struct {
	Origin    string   `json:"origin"`
	PinnedIPs []string `json:"pinnedIps"`
}

func (s *WorkspaceStore) AuthorizeBrowserOrigin(ctx context.Context, workspaceID, rawOrigin string) (BrowserOriginRecord, error) {
	normalized, host, err := normalizeBrowserOrigin(rawOrigin)
	if err != nil {
		return BrowserOriginRecord{}, err
	}
	pinned, err := resolveBrowserHost(ctx, host)
	if err != nil {
		return BrowserOriginRecord{}, err
	}
	now := time.Now().UTC()
	record := BrowserOriginRecord{Origin: normalized, PinnedIPs: pinned}

	registry, err := s.load()
	if err != nil {
		return BrowserOriginRecord{}, err
	}
	for i := range registry.Workspaces {
		if registry.Workspaces[i].WorkspaceID != workspaceID {
			continue
		}
		if !registry.Workspaces[i].Enabled {
			return BrowserOriginRecord{}, ErrWorkspaceDisabled
		}
		origins := make([]BrowserOriginRecord, 0, len(registry.Workspaces[i].BrowserOrigins)+1)
		replaced := false
		for _, existing := range registry.Workspaces[i].BrowserOrigins {
			if existing.Origin == normalized {
				origins = append(origins, record)
				replaced = true
				continue
			}
			origins = append(origins, existing)
		}
		if !replaced {
			if len(origins) >= maxBrowserOrigins {
				return BrowserOriginRecord{}, fmt.Errorf("workspace browser origin limit reached")
			}
			origins = append(origins, record)
		}
		sort.Slice(origins, func(a, b int) bool { return origins[a].Origin < origins[b].Origin })
		registry.Workspaces[i].BrowserOrigins = origins
		registry.Workspaces[i].Revision++
		registry.Workspaces[i].UpdatedAt = now
		if err := s.save(registry); err != nil {
			return BrowserOriginRecord{}, err
		}
		return record, nil
	}
	return BrowserOriginRecord{}, ErrWorkspaceNotFound
}

func (s *WorkspaceStore) RevokeBrowserOrigin(workspaceID, rawOrigin string) error {
	normalized, _, err := normalizeBrowserOrigin(rawOrigin)
	if err != nil {
		return err
	}
	registry, err := s.load()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for i := range registry.Workspaces {
		if registry.Workspaces[i].WorkspaceID != workspaceID {
			continue
		}
		origins := registry.Workspaces[i].BrowserOrigins[:0]
		removed := false
		for _, existing := range registry.Workspaces[i].BrowserOrigins {
			if existing.Origin == normalized {
				removed = true
				continue
			}
			origins = append(origins, existing)
		}
		if !removed {
			return ErrBrowserOriginDenied
		}
		registry.Workspaces[i].BrowserOrigins = append([]BrowserOriginRecord(nil), origins...)
		registry.Workspaces[i].Revision++
		registry.Workspaces[i].UpdatedAt = now
		return s.save(registry)
	}
	return ErrWorkspaceNotFound
}

func (s *WorkspaceStore) ActiveBrowserOrigins(workspaceID string) ([]BrowserOriginRecord, error) {
	workspace, err := s.Resolve(workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]BrowserOriginRecord, 0, len(workspace.BrowserOrigins))
	for _, origin := range workspace.BrowserOrigins {
		copyRecord := origin
		copyRecord.PinnedIPs = append([]string(nil), origin.PinnedIPs...)
		out = append(out, copyRecord)
	}
	return out, nil
}

func (s *WorkspaceStore) ValidateBrowserURL(ctx context.Context, workspaceID, rawURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil {
		return "", ErrBrowserOriginDenied
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", ErrBrowserOriginDenied
	}
	if u.Fragment != "" {
		u.Fragment = ""
	}
	origin, host, err := normalizeBrowserOrigin(u.Scheme + "://" + u.Host)
	if err != nil {
		return "", ErrBrowserOriginDenied
	}
	current, err := resolveBrowserHost(ctx, host)
	if err != nil {
		return "", err
	}
	if !browserAddressesNeedAuthorization(current) {
		return u.String(), nil
	}
	origins, err := s.ActiveBrowserOrigins(workspaceID)
	if err != nil {
		return "", err
	}
	for _, allowed := range origins {
		if allowed.Origin != origin {
			continue
		}
		if !sameStringSet(current, allowed.PinnedIPs) {
			return "", ErrBrowserDNSChanged
		}
		return u.String(), nil
	}
	return "", ErrBrowserOriginDenied
}

func normalizeBrowserOrigin(raw string) (origin string, host string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", "", fmt.Errorf("browser origin must be an absolute http(s) origin")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", "", fmt.Errorf("browser origin must not contain credentials, path, query, or fragment")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", "", fmt.Errorf("browser origin scheme must be http or https")
	}
	host = strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || strings.Contains(host, "%") {
		return "", "", fmt.Errorf("browser origin host is invalid")
	}
	for _, r := range host {
		if r > 127 {
			return "", "", fmt.Errorf("browser origin host must use ASCII/punycode form")
		}
	}
	port := u.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", "", fmt.Errorf("browser origin port is invalid")
	}
	return scheme + "://" + net.JoinHostPort(host, port), host, nil
}

func resolveBrowserHost(ctx context.Context, host string) ([]string, error) {
	var ips []net.IP
	if literal := net.ParseIP(host); literal != nil {
		ips = []net.IP{literal}
	} else {
		resolved, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve browser origin: %w", err)
		}
		ips = resolved
	}
	set := make(map[string]struct{}, len(ips))
	for _, ip := range ips {
		if !browserIPAllowed(ip) {
			return nil, ErrBrowserOriginUnsafe
		}
		set[ip.String()] = struct{}{}
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("browser origin resolved to no addresses")
	}
	out := make([]string, 0, len(set))
	for ip := range set {
		out = append(out, ip)
	}
	sort.Strings(out)
	return out, nil
}

func browserIPAllowed(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	for _, blocked := range []string{"100.100.100.200"} {
		if ip.Equal(net.ParseIP(blocked)) {
			return false
		}
	}
	return true
}

func browserAddressesNeedAuthorization(addresses []string) bool {
	for _, value := range addresses {
		ip := net.ParseIP(value)
		if ip == nil {
			return true
		}
		if ip.IsLoopback() || ip.IsPrivate() || isCGNAT(ip) {
			return true
		}
	}
	return false
}

func isCGNAT(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	return v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	left := append([]string(nil), a...)
	right := append([]string(nil), b...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
