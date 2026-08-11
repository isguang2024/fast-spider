package opsbackup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	MaxStagingPruneCandidates = 256
	MaxStagingPruneFiles      = 10_000
	MaxStagingPruneBytes      = int64(16 << 30)
	MaxStagingPruneDepth      = 32
)

type StagingLayout string

const (
	StagingLayoutLocal  StagingLayout = "local"
	StagingLayoutServer StagingLayout = "server"
)

type StagingPruneOptions struct {
	Directory      string
	Layout         StagingLayout
	ThroughVersion string
	Apply          bool
}

type StagingPruneItem struct {
	BaseName       string `json:"basename"`
	Version        string `json:"version"`
	EstimatedBytes int64  `json:"estimatedBytes"`
}

type StagingPruneResult struct {
	CandidateCount int                `json:"candidateCount"`
	PlannedCount   int                `json:"plannedCount"`
	RetainedCount  int                `json:"retainedCount"`
	DeletedCount   int                `json:"deletedCount"`
	EstimatedBytes int64              `json:"estimatedBytes"`
	Planned        []StagingPruneItem `json:"planned"`
	Retained       []StagingPruneItem `json:"retained"`
	Deleted        []StagingPruneItem `json:"deleted"`
}

var (
	stagingSemverPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	stagingCommitPattern = `[0-9A-Fa-f]{7,40}`
	stagingNamePatterns  = map[StagingLayout]*regexp.Regexp{
		StagingLayoutLocal:  regexp.MustCompile(`^release-((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))(?:-(` + stagingCommitPattern + `))?$`),
		StagingLayoutServer: regexp.MustCompile(`^fast-spider-((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))(?:-(` + stagingCommitPattern + `))?$`),
	}
)

type stagingVersion struct {
	raw   string
	parts [3]string
}

type stagingPruneCandidate struct {
	item    StagingPruneItem
	path    string
	version stagingVersion
	info    os.FileInfo
}

type stagingScanLimits struct {
	maxFiles int
	maxBytes int64
	maxDepth int
}

type stagingScanState struct {
	files int
	bytes int64
}

type stagingPruneDependencies struct {
	lstat         func(string) (os.FileInfo, error)
	readDir       func(string) ([]os.DirEntry, error)
	isReparse     func(string, os.FileInfo) (bool, error)
	remove        func(string) error
	beforeRecheck func()
	limits        stagingScanLimits
}

type stagingPruneError struct {
	public string
	cause  error
}

func (err *stagingPruneError) Error() string { return err.public }
func (err *stagingPruneError) Unwrap() error { return err.cause }

func stagingError(public string, cause error) error {
	return &stagingPruneError{public: public, cause: cause}
}

// PruneReleaseStaging plans or applies bounded cleanup of standard release
// staging directories. It never creates the root and never follows reparse
// points or symbolic links.
func PruneReleaseStaging(ctx context.Context, options StagingPruneOptions) (StagingPruneResult, error) {
	return pruneReleaseStaging(ctx, options, stagingPruneDependencies{
		lstat:     os.Lstat,
		readDir:   os.ReadDir,
		isReparse: releaseBackupPathIsReparse,
		remove:    os.Remove,
		limits: stagingScanLimits{
			maxFiles: MaxStagingPruneFiles,
			maxBytes: MaxStagingPruneBytes,
			maxDepth: MaxStagingPruneDepth,
		},
	})
}

func pruneReleaseStaging(ctx context.Context, options StagingPruneOptions, deps stagingPruneDependencies) (StagingPruneResult, error) {
	result := emptyStagingPruneResult()
	through, err := parseStagingVersion(options.ThroughVersion)
	if err != nil {
		return result, errors.New("through version must be a three-part semantic version")
	}
	namePattern, ok := stagingNamePatterns[options.Layout]
	if !ok {
		return result, errors.New("staging layout must be local or server")
	}
	if strings.TrimSpace(options.Directory) == "" || !filepath.IsAbs(options.Directory) {
		return result, errors.New("staging directory must be an absolute path")
	}
	root := filepath.Clean(options.Directory)
	rootInfo, err := deps.lstat(root)
	if err != nil {
		return result, stagingError("inspect staging root failed", err)
	}
	rootReparse, err := deps.isReparse(root, rootInfo)
	if err != nil {
		return result, stagingError("inspect staging root attributes failed", err)
	}
	if rootReparse || !rootInfo.IsDir() {
		return result, errors.New("staging root must be an existing non-reparse directory")
	}
	entries, err := deps.readDir(root)
	if err != nil {
		return result, stagingError("read staging root failed", err)
	}

	candidates := make([]stagingPruneCandidate, 0)
	for _, entry := range entries {
		matches := namePattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, inspectErr := deps.lstat(path)
		if inspectErr != nil {
			return result, stagingError(fmt.Sprintf("inspect staging candidate %q failed", entry.Name()), inspectErr)
		}
		reparse, inspectErr := deps.isReparse(path, info)
		if inspectErr != nil {
			return result, stagingError(fmt.Sprintf("inspect staging candidate %q attributes failed", entry.Name()), inspectErr)
		}
		if reparse {
			return result, fmt.Errorf("staging candidate %q is a reparse point", entry.Name())
		}
		// Matching regular files and other non-directory entries are preserved.
		if !info.IsDir() {
			continue
		}
		version, parseErr := parseStagingVersion(matches[1])
		if parseErr != nil {
			return result, fmt.Errorf("staging candidate %q has an invalid version", entry.Name())
		}
		candidates = append(candidates, stagingPruneCandidate{
			item:    StagingPruneItem{BaseName: entry.Name(), Version: version.raw},
			path:    path,
			version: version,
			info:    info,
		})
		if len(candidates) > MaxStagingPruneCandidates {
			return stagingPlanningFailure(candidates), fmt.Errorf("staging candidate count exceeds %d", MaxStagingPruneCandidates)
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if comparison := compareStagingVersions(candidates[i].version, candidates[j].version); comparison != 0 {
			return comparison < 0
		}
		return candidates[i].item.BaseName < candidates[j].item.BaseName
	})
	result.CandidateCount = len(candidates)
	if len(candidates) == 0 {
		return result, nil
	}

	scanState := stagingScanState{}
	for index := range candidates {
		if err := ctx.Err(); err != nil {
			return stagingPlanningFailure(candidates), err
		}
		bytes, scanErr := scanStagingTree(ctx, candidates[index].path, 0, deps, &scanState)
		if scanErr != nil {
			return stagingPlanningFailure(candidates), stagingError(fmt.Sprintf("scan staging candidate %q failed", candidates[index].item.BaseName), scanErr)
		}
		candidates[index].item.EstimatedBytes = bytes
	}

	for _, candidate := range candidates {
		if compareStagingVersions(candidate.version, through) <= 0 {
			result.Planned = append(result.Planned, candidate.item)
			result.EstimatedBytes += candidate.item.EstimatedBytes
		} else {
			result.Retained = append(result.Retained, candidate.item)
		}
	}
	result.PlannedCount = len(result.Planned)
	result.RetainedCount = len(result.Retained)
	if !options.Apply || result.PlannedCount == 0 {
		return result, nil
	}

	if deps.beforeRecheck != nil {
		deps.beforeRecheck()
	}
	// Recheck all recognized candidate identities and all planned trees before
	// the first deletion. Any planning/recheck anomaly therefore deletes zero.
	recheckState := stagingScanState{}
	for _, candidate := range candidates {
		info, inspectErr := deps.lstat(candidate.path)
		if inspectErr != nil {
			return stagingPlanningFailure(candidates), stagingError(fmt.Sprintf("recheck staging candidate %q failed", candidate.item.BaseName), inspectErr)
		}
		reparse, inspectErr := deps.isReparse(candidate.path, info)
		if inspectErr != nil {
			return stagingPlanningFailure(candidates), stagingError(fmt.Sprintf("recheck staging candidate %q attributes failed", candidate.item.BaseName), inspectErr)
		}
		if reparse || !info.IsDir() || !os.SameFile(candidate.info, info) || candidate.info.Size() != info.Size() || !candidate.info.ModTime().Equal(info.ModTime()) {
			return stagingPlanningFailure(candidates), fmt.Errorf("staging candidate %q changed during planning", candidate.item.BaseName)
		}
		if compareStagingVersions(candidate.version, through) <= 0 {
			bytes, scanErr := scanStagingTree(ctx, candidate.path, 0, deps, &recheckState)
			if scanErr != nil || bytes != candidate.item.EstimatedBytes {
				if scanErr != nil {
					return stagingPlanningFailure(candidates), stagingError(fmt.Sprintf("recheck staging candidate %q tree failed", candidate.item.BaseName), scanErr)
				}
				return stagingPlanningFailure(candidates), fmt.Errorf("staging candidate %q contents changed during planning", candidate.item.BaseName)
			}
		}
	}

	result.Deleted = make([]StagingPruneItem, 0, result.PlannedCount)
	var removeErr error
	for _, candidate := range candidates {
		if compareStagingVersions(candidate.version, through) > 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			result.Retained = append(result.Retained, candidate.item)
			removeErr = errors.Join(removeErr, err)
			continue
		}
		if err := removeStagingTree(candidate.path, candidate.item.BaseName, 0, deps); err != nil {
			result.Retained = append(result.Retained, candidate.item)
			removeErr = errors.Join(removeErr, err)
			continue
		}
		result.Deleted = append(result.Deleted, candidate.item)
	}
	result.DeletedCount = len(result.Deleted)
	result.RetainedCount = len(result.Retained)
	return result, removeErr
}

func emptyStagingPruneResult() StagingPruneResult {
	return StagingPruneResult{
		Planned:  []StagingPruneItem{},
		Retained: []StagingPruneItem{},
		Deleted:  []StagingPruneItem{},
	}
}

func stagingPlanningFailure(candidates []stagingPruneCandidate) StagingPruneResult {
	result := emptyStagingPruneResult()
	result.CandidateCount = len(candidates)
	for _, candidate := range candidates {
		result.Retained = append(result.Retained, candidate.item)
	}
	result.RetainedCount = len(result.Retained)
	return result
}

func parseStagingVersion(value string) (stagingVersion, error) {
	matches := stagingSemverPattern.FindStringSubmatch(value)
	if matches == nil {
		return stagingVersion{}, errors.New("invalid three-part semantic version")
	}
	return stagingVersion{raw: value, parts: [3]string{matches[1], matches[2], matches[3]}}, nil
}

func compareStagingVersions(left, right stagingVersion) int {
	for index := range left.parts {
		if len(left.parts[index]) != len(right.parts[index]) {
			if len(left.parts[index]) < len(right.parts[index]) {
				return -1
			}
			return 1
		}
		if left.parts[index] < right.parts[index] {
			return -1
		}
		if left.parts[index] > right.parts[index] {
			return 1
		}
	}
	return 0
}

func scanStagingTree(ctx context.Context, path string, depth int, deps stagingPruneDependencies, state *stagingScanState) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if depth > deps.limits.maxDepth {
		return 0, fmt.Errorf("tree depth exceeds %d", deps.limits.maxDepth)
	}
	info, err := deps.lstat(path)
	if err != nil {
		return 0, err
	}
	reparse, err := deps.isReparse(path, info)
	if err != nil {
		return 0, err
	}
	if reparse {
		return 0, errors.New("tree contains a reparse point")
	}
	if info.Mode().IsRegular() {
		state.files++
		if state.files > deps.limits.maxFiles {
			return 0, fmt.Errorf("tree file count exceeds %d", deps.limits.maxFiles)
		}
		if info.Size() < 0 || state.bytes > deps.limits.maxBytes-info.Size() {
			return 0, fmt.Errorf("tree bytes exceed %d", deps.limits.maxBytes)
		}
		state.bytes += info.Size()
		return info.Size(), nil
	}
	if !info.IsDir() {
		return 0, errors.New("tree contains a non-regular entry")
	}
	entries, err := deps.readDir(path)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, entry := range entries {
		bytes, scanErr := scanStagingTree(ctx, filepath.Join(path, entry.Name()), depth+1, deps, state)
		if scanErr != nil {
			return 0, scanErr
		}
		total += bytes
	}
	return total, nil
}

func removeStagingTree(path, candidateName string, depth int, deps stagingPruneDependencies) error {
	if depth > deps.limits.maxDepth {
		return fmt.Errorf("remove staging candidate %q exceeded depth bound", candidateName)
	}
	info, err := deps.lstat(path)
	if err != nil {
		return stagingError(fmt.Sprintf("remove staging candidate %q inspection failed", candidateName), err)
	}
	reparse, err := deps.isReparse(path, info)
	if err != nil {
		return stagingError(fmt.Sprintf("remove staging candidate %q attribute inspection failed", candidateName), err)
	}
	if reparse {
		return fmt.Errorf("remove staging candidate %q refused a reparse point", candidateName)
	}
	if info.IsDir() {
		entries, readErr := deps.readDir(path)
		if readErr != nil {
			return stagingError(fmt.Sprintf("remove staging candidate %q enumeration failed", candidateName), readErr)
		}
		for _, entry := range entries {
			if err := removeStagingTree(filepath.Join(path, entry.Name()), candidateName, depth+1, deps); err != nil {
				return err
			}
		}
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("remove staging candidate %q refused a non-regular entry", candidateName)
	}
	if err := deps.remove(path); err != nil {
		return stagingError(fmt.Sprintf("remove staging candidate %q failed", candidateName), err)
	}
	return nil
}
