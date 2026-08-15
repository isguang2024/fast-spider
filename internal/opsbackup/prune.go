package opsbackup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	MaxReleaseBackupKeep       = 100
	maxReleaseBackupCandidates = 256
)

var releaseBackupNamePattern = regexp.MustCompile(`^pre-(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)-[0-9A-Fa-f]{7,40}\.zip$`)

type PruneResult struct {
	CandidateCount int      `json:"candidateCount"`
	KeptCount      int      `json:"keptCount"`
	PlannedCount   int      `json:"plannedCount"`
	DeletedCount   int      `json:"deletedCount"`
	Applied        bool     `json:"applied"`
	Kept           []string `json:"kept"`
	Planned        []string `json:"planned"`
	Deleted        []string `json:"deleted"`
}

type releaseBackupCandidate struct {
	name      string
	path      string
	info      os.FileInfo
	identity  releaseBackupFileIdentity
	createdAt time.Time
}

type releaseBackupDirectoryLock struct {
	mu   sync.Mutex
	refs int
}

var releaseBackupDirectoryLocks struct {
	mu      sync.Mutex
	entries map[string]*releaseBackupDirectoryLock
}

type releaseBackupPruneDependencies struct {
	verify     func(context.Context, string) (Manifest, error)
	isReparse  func(string, os.FileInfo) (bool, error)
	removeFile func(string) error
}

// PruneReleaseBackups verifies every standard release backup and plans the
// candidates older than the requested keep count. Files are removed only when
// apply is true, so unattended callers can safely inspect the default result.
func PruneReleaseBackups(ctx context.Context, directory string, keep int, apply bool) (PruneResult, error) {
	return pruneReleaseBackups(ctx, directory, keep, apply, releaseBackupPruneDependencies{
		verify:     Verify,
		isReparse:  releaseBackupPathIsReparse,
		removeFile: os.Remove,
	})
}

func pruneReleaseBackups(ctx context.Context, directory string, keep int, apply bool, deps releaseBackupPruneDependencies) (PruneResult, error) {
	if keep < 1 || keep > MaxReleaseBackupKeep {
		return PruneResult{}, fmt.Errorf("release backup keep must be between 1 and %d", MaxReleaseBackupKeep)
	}
	if directory == "" || !filepath.IsAbs(directory) {
		return PruneResult{}, errors.New("release backup directory must be an absolute path")
	}
	directory = filepath.Clean(directory)
	rootInfo, err := os.Lstat(directory)
	if err != nil {
		return PruneResult{}, fmt.Errorf("inspect release backup directory: %w", err)
	}
	rootReparse, err := deps.isReparse(directory, rootInfo)
	if err != nil {
		return PruneResult{}, fmt.Errorf("inspect release backup directory attributes: %w", err)
	}
	if rootReparse || !rootInfo.IsDir() {
		return PruneResult{}, errors.New("release backup root must be a regular non-reparse directory")
	}
	unlockDirectory := lockReleaseBackupDirectory(directory)
	defer unlockDirectory()
	entries, err := os.ReadDir(directory)
	if err != nil {
		return PruneResult{}, fmt.Errorf("read release backup directory: %w", err)
	}

	candidates := make([]releaseBackupCandidate, 0)
	for _, entry := range entries {
		if !releaseBackupNamePattern.MatchString(entry.Name()) {
			continue
		}
		candidates = append(candidates, releaseBackupCandidate{
			name: entry.Name(),
			path: filepath.Join(directory, entry.Name()),
		})
		if len(candidates) > maxReleaseBackupCandidates {
			return PruneResult{CandidateCount: len(candidates), Applied: apply, Kept: []string{}, Planned: []string{}, Deleted: []string{}}, fmt.Errorf("release backup candidate count exceeds %d", maxReleaseBackupCandidates)
		}
	}
	if len(candidates) == 0 {
		return PruneResult{Applied: apply, Kept: []string{}, Planned: []string{}, Deleted: []string{}}, nil
	}

	failureResult := func() PruneResult {
		names := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			names = append(names, candidate.name)
		}
		sort.Strings(names)
		return PruneResult{CandidateCount: len(candidates), KeptCount: len(names), Applied: apply, Kept: names, Planned: []string{}, Deleted: []string{}}
	}

	for index := range candidates {
		if err := ctx.Err(); err != nil {
			return failureResult(), err
		}
		candidate := &candidates[index]
		pathInfo, err := os.Lstat(candidate.path)
		if err != nil {
			return failureResult(), fmt.Errorf("inspect release backup candidate %q: %w", candidate.name, err)
		}
		reparse, err := deps.isReparse(candidate.path, pathInfo)
		if err != nil {
			return failureResult(), fmt.Errorf("inspect release backup candidate attributes %q: %w", candidate.name, err)
		}
		if reparse || !pathInfo.Mode().IsRegular() {
			return failureResult(), fmt.Errorf("release backup candidate %q is not a regular non-reparse file", candidate.name)
		}
		info, identity, err := inspectReleaseBackupCandidate(candidate.path)
		if err != nil {
			return failureResult(), fmt.Errorf("inspect release backup candidate identity %q: %w", candidate.name, err)
		}
		if !info.Mode().IsRegular() {
			return failureResult(), fmt.Errorf("release backup candidate %q is not a regular non-reparse file", candidate.name)
		}
		manifest, err := deps.verify(ctx, candidate.path)
		if err != nil {
			return failureResult(), fmt.Errorf("verify release backup candidate %q: %w", candidate.name, err)
		}
		createdAt, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt)
		if err != nil {
			return failureResult(), fmt.Errorf("parse release backup creation time %q: %w", candidate.name, err)
		}
		candidate.info = info
		candidate.identity = identity
		candidate.createdAt = createdAt.UTC()
	}

	// Recheck every planned candidate before the first remove so a path/type swap
	// observed during planning remains a zero-deletion failure.
	for _, candidate := range candidates {
		if err := recheckReleaseBackupCandidate(candidate, deps); err != nil {
			return failureResult(), err
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].createdAt.Equal(candidates[j].createdAt) {
			return candidates[i].name < candidates[j].name
		}
		return candidates[i].createdAt.After(candidates[j].createdAt)
	})
	keepCount := keep
	if keepCount > len(candidates) {
		keepCount = len(candidates)
	}
	result := PruneResult{
		CandidateCount: len(candidates),
		Applied:        apply,
		Kept:           make([]string, 0, len(candidates)),
		Planned:        make([]string, 0, len(candidates)-keepCount),
		Deleted:        make([]string, 0, len(candidates)-keepCount),
	}
	for _, candidate := range candidates[:keepCount] {
		result.Kept = append(result.Kept, candidate.name)
	}
	for _, candidate := range candidates[keepCount:] {
		result.Planned = append(result.Planned, candidate.name)
	}
	result.PlannedCount = len(result.Planned)
	if !apply {
		result.KeptCount = len(result.Kept)
		return result, nil
	}
	var removeErr error
	plannedCandidates := candidates[keepCount:]
	for index, candidate := range plannedCandidates {
		if err := ctx.Err(); err != nil {
			for _, retained := range plannedCandidates[index:] {
				result.Kept = append(result.Kept, retained.name)
			}
			result.KeptCount = len(result.Kept)
			result.DeletedCount = len(result.Deleted)
			return result, errors.Join(removeErr, err)
		}
		// The batch recheck above guarantees a zero-deletion failure when the
		// plan is already stale. Rechecking immediately before each remove also
		// protects later candidates while earlier removals are in progress.
		if err := recheckReleaseBackupCandidate(candidate, deps); err != nil {
			for _, retained := range plannedCandidates[index:] {
				result.Kept = append(result.Kept, retained.name)
			}
			result.KeptCount = len(result.Kept)
			result.DeletedCount = len(result.Deleted)
			return result, errors.Join(removeErr, err)
		}
		if err := deps.removeFile(candidate.path); err != nil {
			result.Kept = append(result.Kept, candidate.name)
			removeErr = errors.Join(removeErr, fmt.Errorf("remove release backup %q: %w", candidate.name, err))
			continue
		}
		result.Deleted = append(result.Deleted, candidate.name)
	}
	result.KeptCount = len(result.Kept)
	result.DeletedCount = len(result.Deleted)
	return result, removeErr
}

func recheckReleaseBackupCandidate(candidate releaseBackupCandidate, deps releaseBackupPruneDependencies) error {
	pathInfo, err := os.Lstat(candidate.path)
	if err != nil {
		return fmt.Errorf("recheck release backup candidate %q: %w", candidate.name, err)
	}
	reparse, err := deps.isReparse(candidate.path, pathInfo)
	if err != nil {
		return fmt.Errorf("recheck release backup candidate attributes %q: %w", candidate.name, err)
	}
	if reparse || !pathInfo.Mode().IsRegular() {
		return fmt.Errorf("release backup candidate %q changed during planning", candidate.name)
	}
	info, identity, err := inspectReleaseBackupCandidate(candidate.path)
	if err != nil {
		return fmt.Errorf("recheck release backup candidate identity %q: %w", candidate.name, err)
	}
	if !info.Mode().IsRegular() || candidate.identity != identity || candidate.info.Size() != info.Size() || !candidate.info.ModTime().Equal(info.ModTime()) {
		return fmt.Errorf("release backup candidate %q changed during planning", candidate.name)
	}
	return nil
}

func inspectReleaseBackupCandidate(path string) (os.FileInfo, releaseBackupFileIdentity, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, releaseBackupFileIdentity{}, err
	}
	info, statErr := file.Stat()
	if statErr != nil {
		file.Close()
		return nil, releaseBackupFileIdentity{}, statErr
	}
	identity, identityErr := releaseBackupFileIdentityForFile(file, info)
	closeErr := file.Close()
	if identityErr != nil {
		return nil, releaseBackupFileIdentity{}, identityErr
	}
	if closeErr != nil {
		return nil, releaseBackupFileIdentity{}, closeErr
	}
	return info, identity, nil
}

func lockReleaseBackupDirectory(directory string) func() {
	key := filepath.Clean(directory)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	releaseBackupDirectoryLocks.mu.Lock()
	if releaseBackupDirectoryLocks.entries == nil {
		releaseBackupDirectoryLocks.entries = make(map[string]*releaseBackupDirectoryLock)
	}
	lock := releaseBackupDirectoryLocks.entries[key]
	if lock == nil {
		lock = &releaseBackupDirectoryLock{}
		releaseBackupDirectoryLocks.entries[key] = lock
	}
	lock.refs++
	releaseBackupDirectoryLocks.mu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		releaseBackupDirectoryLocks.mu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(releaseBackupDirectoryLocks.entries, key)
		}
		releaseBackupDirectoryLocks.mu.Unlock()
	}
}
