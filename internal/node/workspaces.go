package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	"github.com/isguang2024/fast-spider/internal/security"
)

var (
	ErrWorkspaceNotFound = errors.New("workspace not found")
	ErrWorkspaceDisabled = errors.New("workspace disabled")
)

const (
	WorkspacePermissionRead       = "read"
	WorkspacePermissionWrite      = "write"
	WorkspacePermissionShell      = "shell"
	WorkspacePermissionGitWrite   = "git-write"
	WorkspacePermissionGitNetwork = "git-network"
	WorkspacePermissionGitHooks   = "git-hooks"
	WorkspacePermissionBuild      = "build"
)

type BuildProfileRecord struct {
	ProfileID      string   `json:"profileId"`
	DisplayName    string   `json:"displayName"`
	Argv           []string `json:"argv"`
	Cwd            string   `json:"cwd"`
	TimeoutSeconds int64    `json:"timeoutSeconds"`
}

type WorkspaceRecord struct {
	WorkspaceID    string                `json:"workspaceId"`
	DisplayName    string                `json:"displayName"`
	Root           string                `json:"root"`
	Enabled        bool                  `json:"enabled"`
	Revision       int64                 `json:"revision"`
	Permissions    []string              `json:"permissions"`
	BuildProfiles  []BuildProfileRecord  `json:"buildProfiles,omitempty"`
	BrowserOrigins []BrowserOriginRecord `json:"browserOrigins,omitempty"`
	CreatedAt      time.Time             `json:"createdAt"`
	UpdatedAt      time.Time             `json:"updatedAt"`
}

type workspaceRegistryFile struct {
	Version    int               `json:"version"`
	Workspaces []WorkspaceRecord `json:"workspaces"`
}

type LocalWorkspaceSummary struct {
	WorkspaceID     string                 `json:"workspaceId"`
	DisplayName     string                 `json:"displayName"`
	Root            string                 `json:"root"`
	Enabled         bool                   `json:"enabled"`
	Revision        int64                  `json:"revision"`
	Permissions     []string               `json:"permissions"`
	BuildProfileIDs []string               `json:"buildProfileIds,omitempty"`
	BrowserOrigins  []BrowserOriginSummary `json:"browserOrigins,omitempty"`
}

type WorkspaceStore struct{ path string }

func NewWorkspaceStore(dataDir string) *WorkspaceStore {
	return &WorkspaceStore{path: filepath.Join(dataDir, "workspaces.json")}
}

func (s *WorkspaceStore) Add(root, displayName string) (WorkspaceRecord, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return WorkspaceRecord{}, err
	}
	real, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return WorkspaceRecord{}, fmt.Errorf("resolve workspace root: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return WorkspaceRecord{}, fmt.Errorf("workspace root must be an existing directory")
	}
	registry, err := s.load()
	if err != nil {
		return WorkspaceRecord{}, err
	}
	for _, item := range registry.Workspaces {
		if samePath(item.Root, real) {
			return WorkspaceRecord{}, fmt.Errorf("workspace root already registered as %s", item.WorkspaceID)
		}
	}
	if strings.TrimSpace(displayName) == "" {
		displayName = filepath.Base(real)
	}
	if len(displayName) > 128 {
		return WorkspaceRecord{}, fmt.Errorf("workspace display name is too long")
	}
	id, err := security.RandomOpaque("ws_")
	if err != nil {
		return WorkspaceRecord{}, err
	}
	now := time.Now().UTC()
	record := WorkspaceRecord{WorkspaceID: id, DisplayName: displayName, Root: real, Enabled: true, Revision: 1, Permissions: []string{WorkspacePermissionRead}, CreatedAt: now, UpdatedAt: now}
	registry.Workspaces = append(registry.Workspaces, record)
	if err := s.save(registry); err != nil {
		return WorkspaceRecord{}, err
	}
	return record, nil
}

func (s *WorkspaceStore) List() ([]protocolv1.WorkspaceSummary, error) {
	registry, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]protocolv1.WorkspaceSummary, 0, len(registry.Workspaces))
	for _, item := range registry.Workspaces {
		out = append(out, protocolv1.WorkspaceSummary{WorkspaceId: item.WorkspaceID, DisplayName: item.DisplayName, Enabled: item.Enabled, Revision: item.Revision, Permissions: append([]string(nil), item.Permissions...)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DisplayName < out[j].DisplayName })
	return out, nil
}

func (s *WorkspaceStore) ListLocal() ([]LocalWorkspaceSummary, error) {
	registry, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]LocalWorkspaceSummary, 0, len(registry.Workspaces))
	for _, item := range registry.Workspaces {
		profiles := make([]string, 0, len(item.BuildProfiles))
		for _, profile := range item.BuildProfiles {
			profiles = append(profiles, profile.ProfileID)
		}
		sort.Strings(profiles)
		origins := make([]BrowserOriginSummary, 0, len(item.BrowserOrigins))
		for _, origin := range item.BrowserOrigins {
			origins = append(origins, BrowserOriginSummary{Origin: origin.Origin, PinnedIPs: append([]string(nil), origin.PinnedIPs...)})
		}
		out = append(out, LocalWorkspaceSummary{
			WorkspaceID: item.WorkspaceID, DisplayName: item.DisplayName, Root: item.Root, Enabled: item.Enabled,
			Revision: item.Revision, Permissions: append([]string(nil), item.Permissions...), BuildProfileIDs: profiles, BrowserOrigins: origins,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DisplayName < out[j].DisplayName })
	return out, nil
}

func (s *WorkspaceStore) SetEnabled(workspaceID string, enabled bool) error {
	registry, err := s.load()
	if err != nil {
		return err
	}
	for i := range registry.Workspaces {
		if registry.Workspaces[i].WorkspaceID == workspaceID {
			registry.Workspaces[i].Enabled = enabled
			registry.Workspaces[i].Revision++
			registry.Workspaces[i].UpdatedAt = time.Now().UTC()
			return s.save(registry)
		}
	}
	return ErrWorkspaceNotFound
}

func (s *WorkspaceStore) SetPermissions(workspaceID string, permissions []string) error {
	normalized, err := normalizeWorkspacePermissions(permissions)
	if err != nil {
		return err
	}
	registry, err := s.load()
	if err != nil {
		return err
	}
	for i := range registry.Workspaces {
		if registry.Workspaces[i].WorkspaceID == workspaceID {
			registry.Workspaces[i].Permissions = normalized
			registry.Workspaces[i].Revision++
			registry.Workspaces[i].UpdatedAt = time.Now().UTC()
			return s.save(registry)
		}
	}
	return ErrWorkspaceNotFound
}

func (s *WorkspaceStore) SetBuildProfile(workspaceID string, profile BuildProfileRecord) error {
	profile.ProfileID = strings.TrimSpace(profile.ProfileID)
	profile.DisplayName = strings.TrimSpace(profile.DisplayName)
	profile.Cwd = strings.TrimSpace(profile.Cwd)
	if profile.ProfileID == "" || len(profile.ProfileID) > 64 {
		return fmt.Errorf("profileId is required and must be at most 64 characters")
	}
	for _, r := range profile.ProfileID {
		if !(r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return fmt.Errorf("profileId contains unsupported characters")
		}
	}
	if profile.DisplayName == "" {
		profile.DisplayName = profile.ProfileID
	}
	if len(profile.DisplayName) > 128 {
		return fmt.Errorf("profile display name is too long")
	}
	if profile.Cwd == "" {
		profile.Cwd = "."
	}
	if filepath.IsAbs(profile.Cwd) || filepath.VolumeName(profile.Cwd) != "" || profile.Cwd == ".." || strings.HasPrefix(filepath.Clean(profile.Cwd), ".."+string(filepath.Separator)) {
		return ErrPathOutsideWorkspace
	}
	if profile.TimeoutSeconds <= 0 {
		profile.TimeoutSeconds = int64(defaultJobTimeout / time.Second)
	}
	if profile.TimeoutSeconds > int64(maxJobTimeout/time.Second) {
		return fmt.Errorf("profile timeout exceeds limit")
	}
	if err := validateShellSpec(profile.Argv, time.Duration(profile.TimeoutSeconds)*time.Second, "profile_validation_key"); err != nil {
		return err
	}
	registry, err := s.load()
	if err != nil {
		return err
	}
	for i := range registry.Workspaces {
		if registry.Workspaces[i].WorkspaceID != workspaceID {
			continue
		}
		replaced := false
		for p := range registry.Workspaces[i].BuildProfiles {
			if registry.Workspaces[i].BuildProfiles[p].ProfileID == profile.ProfileID {
				registry.Workspaces[i].BuildProfiles[p] = profile
				replaced = true
				break
			}
		}
		if !replaced {
			if len(registry.Workspaces[i].BuildProfiles) >= 32 {
				return fmt.Errorf("workspace build profile limit reached")
			}
			registry.Workspaces[i].BuildProfiles = append(registry.Workspaces[i].BuildProfiles, profile)
		}
		sort.Slice(registry.Workspaces[i].BuildProfiles, func(a, b int) bool {
			return registry.Workspaces[i].BuildProfiles[a].ProfileID < registry.Workspaces[i].BuildProfiles[b].ProfileID
		})
		registry.Workspaces[i].Revision++
		registry.Workspaces[i].UpdatedAt = time.Now().UTC()
		return s.save(registry)
	}
	return ErrWorkspaceNotFound
}

func (s *WorkspaceStore) RemoveBuildProfile(workspaceID, profileID string) error {
	registry, err := s.load()
	if err != nil {
		return err
	}
	for i := range registry.Workspaces {
		if registry.Workspaces[i].WorkspaceID != workspaceID {
			continue
		}
		for p := range registry.Workspaces[i].BuildProfiles {
			if registry.Workspaces[i].BuildProfiles[p].ProfileID == profileID {
				registry.Workspaces[i].BuildProfiles = append(registry.Workspaces[i].BuildProfiles[:p], registry.Workspaces[i].BuildProfiles[p+1:]...)
				registry.Workspaces[i].Revision++
				registry.Workspaces[i].UpdatedAt = time.Now().UTC()
				return s.save(registry)
			}
		}
		return ErrWorkspaceNotFound
	}
	return ErrWorkspaceNotFound
}

func (s *WorkspaceStore) BuildProfiles(workspaceID string) ([]BuildProfileRecord, error) {
	workspace, err := s.Resolve(workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]BuildProfileRecord, len(workspace.BuildProfiles))
	for i, item := range workspace.BuildProfiles {
		out[i] = item
		out[i].Argv = append([]string(nil), item.Argv...)
	}
	return out, nil
}

func (s *WorkspaceStore) BuildProfile(workspaceID, profileID string) (BuildProfileRecord, error) {
	workspace, err := s.Resolve(workspaceID)
	if err != nil {
		return BuildProfileRecord{}, err
	}
	for _, item := range workspace.BuildProfiles {
		if item.ProfileID == profileID {
			item.Argv = append([]string(nil), item.Argv...)
			return item, nil
		}
	}
	return BuildProfileRecord{}, ErrWorkspaceNotFound
}

func (s *WorkspaceStore) Remove(workspaceID string) error {
	registry, err := s.load()
	if err != nil {
		return err
	}
	for i := range registry.Workspaces {
		if registry.Workspaces[i].WorkspaceID == workspaceID {
			registry.Workspaces = append(registry.Workspaces[:i], registry.Workspaces[i+1:]...)
			return s.save(registry)
		}
	}
	return ErrWorkspaceNotFound
}

func (s *WorkspaceStore) Lookup(workspaceID string) (WorkspaceRecord, error) {
	registry, err := s.load()
	if err != nil {
		return WorkspaceRecord{}, err
	}
	for _, item := range registry.Workspaces {
		if item.WorkspaceID == workspaceID {
			return item, nil
		}
	}
	return WorkspaceRecord{}, ErrWorkspaceNotFound
}

func (s *WorkspaceStore) Resolve(workspaceID string) (WorkspaceRecord, error) {
	item, err := s.Lookup(workspaceID)
	if err != nil {
		return WorkspaceRecord{}, err
	}
	if !item.Enabled {
		return WorkspaceRecord{}, ErrWorkspaceDisabled
	}
	return item, nil
}

func (s *WorkspaceStore) load() (workspaceRegistryFile, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return workspaceRegistryFile{Version: 1, Workspaces: []WorkspaceRecord{}}, nil
	}
	if err != nil {
		return workspaceRegistryFile{}, err
	}
	var registry workspaceRegistryFile
	if err := json.Unmarshal(raw, &registry); err != nil {
		return workspaceRegistryFile{}, fmt.Errorf("decode workspace registry: %w", err)
	}
	if registry.Version != 1 {
		return workspaceRegistryFile{}, fmt.Errorf("unsupported workspace registry version %d", registry.Version)
	}
	for i := range registry.Workspaces {
		if len(registry.Workspaces[i].Permissions) == 0 {
			registry.Workspaces[i].Permissions = []string{WorkspacePermissionRead}
		}
	}
	return registry, nil
}

func (s *WorkspaceStore) save(registry workspaceRegistryFile) error {
	raw, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (r WorkspaceRecord) Allows(permission string) bool {
	for _, item := range r.Permissions {
		if item == permission {
			return true
		}
	}
	return false
}

func normalizeWorkspacePermissions(input []string) ([]string, error) {
	seen := map[string]bool{}
	for _, item := range input {
		item = strings.TrimSpace(strings.ToLower(item))
		switch item {
		case WorkspacePermissionRead, WorkspacePermissionWrite, WorkspacePermissionShell, WorkspacePermissionGitWrite, WorkspacePermissionGitNetwork, WorkspacePermissionGitHooks, WorkspacePermissionBuild:
			seen[item] = true
		case "":
		default:
			return nil, fmt.Errorf("unsupported workspace permission %q", item)
		}
	}
	if !seen[WorkspacePermissionRead] {
		seen[WorkspacePermissionRead] = true
	}
	out := make([]string, 0, len(seen))
	for item := range seen {
		out = append(out, item)
	}
	sort.Strings(out)
	return out, nil
}

func samePath(a, b string) bool {
	ca, errA := filepath.Abs(a)
	cb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	if filepath.Separator == '\\' {
		return strings.EqualFold(filepath.Clean(ca), filepath.Clean(cb))
	}
	return filepath.Clean(ca) == filepath.Clean(cb)
}
