package opsbackup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
	DeletedCount   int      `json:"deletedCount"`
	Kept           []string `json:"kept"`
	Deleted        []string `json:"deleted"`
}

type releaseBackupCandidate struct {
	name      string
	path      string
	info      os.FileInfo
	createdAt time.Time
}

type releaseBackupPruneDependencies struct {
	verify     func(context.Context, string) (Manifest, error)
	isReparse  func(string, os.FileInfo) (bool, error)
	removeFile func(string) error
}

// PruneReleaseBackups verifies every standard release backup before deleting
// any of them, then keeps the newest requested count by manifest creation time.
func PruneReleaseBackups(ctx context.Context, directory string, keep int) (PruneResult, error) {
	return pruneReleaseBackups(ctx, directory, keep, releaseBackupPruneDependencies{
		verify:     Verify,
		isReparse:  releaseBackupPathIsReparse,
		removeFile: os.Remove,
	})
}

func pruneReleaseBackups(ctx context.Context, directory string, keep int, deps releaseBackupPruneDependencies) (PruneResult, error) {
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
			return PruneResult{CandidateCount: len(candidates)}, fmt.Errorf("release backup candidate count exceeds %d", maxReleaseBackupCandidates)
		}
	}
	if len(candidates) == 0 {
		return PruneResult{Kept: []string{}, Deleted: []string{}}, nil
	}

	failureResult := func() PruneResult {
		names := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			names = append(names, candidate.name)
		}
		sort.Strings(names)
		return PruneResult{CandidateCount: len(candidates), KeptCount: len(names), Kept: names, Deleted: []string{}}
	}

	for index := range candidates {
		if err := ctx.Err(); err != nil {
			return failureResult(), err
		}
		candidate := &candidates[index]
		info, err := os.Lstat(candidate.path)
		if err != nil {
			return failureResult(), fmt.Errorf("inspect release backup candidate %q: %w", candidate.name, err)
		}
		reparse, err := deps.isReparse(candidate.path, info)
		if err != nil {
			return failureResult(), fmt.Errorf("inspect release backup candidate attributes %q: %w", candidate.name, err)
		}
		if reparse || !info.Mode().IsRegular() {
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
		candidate.createdAt = createdAt.UTC()
	}

	// Recheck every planned candidate before the first remove so a path/type swap
	// observed during planning remains a zero-deletion failure.
	for _, candidate := range candidates {
		info, err := os.Lstat(candidate.path)
		if err != nil {
			return failureResult(), fmt.Errorf("recheck release backup candidate %q: %w", candidate.name, err)
		}
		reparse, err := deps.isReparse(candidate.path, info)
		if err != nil || reparse || !info.Mode().IsRegular() || !os.SameFile(candidate.info, info) || candidate.info.Size() != info.Size() || !candidate.info.ModTime().Equal(info.ModTime()) {
			if err != nil {
				return failureResult(), fmt.Errorf("recheck release backup candidate attributes %q: %w", candidate.name, err)
			}
			return failureResult(), fmt.Errorf("release backup candidate %q changed during planning", candidate.name)
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
		Kept:           make([]string, 0, len(candidates)),
		Deleted:        make([]string, 0, len(candidates)-keepCount),
	}
	for _, candidate := range candidates[:keepCount] {
		result.Kept = append(result.Kept, candidate.name)
	}
	var removeErr error
	for _, candidate := range candidates[keepCount:] {
		if err := ctx.Err(); err != nil {
			result.Kept = append(result.Kept, candidate.name)
			removeErr = errors.Join(removeErr, err)
			continue
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
