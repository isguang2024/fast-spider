package operationlog

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store 操作日志存储器
type Store struct {
	mu       sync.RWMutex
	path     string
	logger   *slog.Logger
	entries  []Entry
	maxItems int
}

// diskState 磁盘持久化格式
type diskState struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

const (
	currentVersion  = 1
	defaultMaxItems = 5000
	logDirName      = "operation-logs"
	logFileName     = "logs.json"
)

// NewStore 创建操作日志存储器
func NewStore(dataDir string, logger *slog.Logger) (*Store, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, os.ErrInvalid
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Store{
		path:     filepath.Join(dataDir, logDirName, logFileName),
		logger:   logger,
		entries:  make([]Entry, 0, 256),
		maxItems: defaultMaxItems,
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	// 初始化时清理一次过期日志
	removed := s.purgeExpiredLocked()
	if removed > 0 {
		s.logger.Info("operation log: purged expired entries on startup", "removed", removed)
		_ = s.saveLocked()
	}
	return s, nil
}

// Append 追加一条日志
func (s *Store) Append(entry Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
	// 超过最大数量时裁剪最旧的
	if len(s.entries) > s.maxItems {
		s.entries = s.entries[len(s.entries)-s.maxItems:]
	}
	if err := s.saveLocked(); err != nil {
		s.logger.Warn("operation log: save failed", "error", err)
	}
}

// Query 查询日志
// level: 可选过滤级别（空字符串表示全部）
// category: 可选过滤分类（空字符串表示全部）
// limit: 返回最大条数，0 表示使用默认值
// offset: 跳过条数
func (s *Store) Query(level Level, category string, limit, offset int) ([]Entry, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if offset < 0 {
		offset = 0
	}

	filtered := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		if level != "" && e.Level != level {
			continue
		}
		if category != "" && !strings.EqualFold(e.Category, category) {
			continue
		}
		filtered = append(filtered, e)
	}

	// 按时间倒序
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Timestamp > filtered[j].Timestamp
	})

	total := len(filtered)
	if offset >= total {
		return []Entry{}, total
	}
	if limit <= 0 {
		limit = 200
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return filtered[offset:end], total
}

// QueryRecent returns a bounded, retention-aware page ordered by timestamp and
// ID descending. beforeTimestamp/beforeID form an exclusive cursor; zero values
// query from the newest entry.
func (s *Store) QueryRecent(level Level, category string, limit int, beforeTimestamp int64, beforeID string) ([]Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 {
		limit = 50
	}
	now := time.Now().UnixMilli()
	cutoff := time.Now().AddDate(0, 0, -RetentionDays).UnixMilli()
	filtered := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		if e.Timestamp < cutoff || e.Timestamp > now+5*60*1000 {
			continue
		}
		if level != "" && e.Level != level {
			continue
		}
		if category != "" && !strings.EqualFold(e.Category, category) {
			continue
		}
		if beforeTimestamp > 0 && (e.Timestamp > beforeTimestamp || (e.Timestamp == beforeTimestamp && e.ID >= beforeID)) {
			continue
		}
		filtered = append(filtered, e)
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].Timestamp != filtered[j].Timestamp {
			return filtered[i].Timestamp > filtered[j].Timestamp
		}
		return filtered[i].ID > filtered[j].ID
	})
	hasMore := len(filtered) > limit
	if hasMore {
		filtered = filtered[:limit]
	}
	return filtered, hasMore
}

// PurgeExpired 清理过期日志，返回被清理的条数
func (s *Store) PurgeExpired() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := s.purgeExpiredLocked()
	if removed > 0 {
		if err := s.saveLocked(); err != nil {
			s.logger.Warn("operation log: save after purge failed", "error", err)
		}
	}
	return removed
}

// Categories 返回所有出现过的日志分类
func (s *Store) Categories() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[string]struct{})
	for _, e := range s.entries {
		if e.Category != "" {
			seen[e.Category] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Stats 返回日志统计信息
func (s *Store) Stats() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stats := map[string]interface{}{
		"total":          len(s.entries),
		"retention_days": RetentionDays,
	}
	byLevel := make(map[string]int)
	byCategory := make(map[string]int)
	var oldest, newest int64
	for _, e := range s.entries {
		byLevel[string(e.Level)]++
		byCategory[e.Category]++
		if oldest == 0 || e.Timestamp < oldest {
			oldest = e.Timestamp
		}
		if e.Timestamp > newest {
			newest = e.Timestamp
		}
	}
	stats["by_level"] = byLevel
	stats["by_category"] = byCategory
	if oldest > 0 {
		stats["oldest"] = time.UnixMilli(oldest).Format(time.RFC3339)
	}
	if newest > 0 {
		stats["newest"] = time.UnixMilli(newest).Format(time.RFC3339)
	}
	return stats
}

// purgeExpiredLocked 清理过期日志（调用方必须持有写锁）
func (s *Store) purgeExpiredLocked() int {
	cutoff := time.Now().AddDate(0, 0, -RetentionDays).UnixMilli()
	original := len(s.entries)
	kept := s.entries[:0]
	for _, e := range s.entries {
		if e.Timestamp >= cutoff {
			kept = append(kept, e)
		}
	}
	s.entries = kept
	return original - len(kept)
}

// load 从磁盘加载日志
func (s *Store) load() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var state diskState
	if err := json.Unmarshal(raw, &state); err != nil {
		s.logger.Warn("operation log: corrupted log file, starting fresh", "error", err)
		return nil
	}
	s.entries = state.Entries
	return nil
}

// saveLocked 保存到磁盘（调用方必须持有写锁）
func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	state := diskState{Version: currentVersion, Entries: s.entries}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, raw, 0o600)
}
