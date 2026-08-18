package operationlog

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// Level 定义日志级别
type Level string

const (
	LevelInfo    Level = "info"
	LevelWarning Level = "warning"
	LevelError   Level = "error"
)

// RetentionDays 日志保留天数
const RetentionDays = 7

// Entry 表示一条操作日志
type Entry struct {
	ID         string                 `json:"id"`
	Timestamp  int64                  `json:"timestamp"` // Unix 毫秒
	Level      Level                  `json:"level"`
	Category   string                 `json:"category"` // 如：http、browser、hub、agent、system
	Action     string                 `json:"action"`   // 如：request、launch、connect、update
	Message    string                 `json:"message"`
	Method     string                 `json:"method,omitempty"`
	Path       string                 `json:"path,omitempty"`
	Status     int                    `json:"status,omitempty"`
	DurationMs int64                  `json:"duration_ms,omitempty"`
	ClientIP   string                 `json:"client_ip,omitempty"`
	Extra      map[string]interface{} `json:"extra,omitempty"`
}

// NewEntry 创建一条新日志条目
func NewEntry(level Level, category, action, message string) Entry {
	now := time.Now().UnixMilli()
	return Entry{
		ID:        generateID(now, category, action, message),
		Timestamp: now,
		Level:     level,
		Category:  category,
		Action:    action,
		Message:   message,
	}
}

// WithHTTP 设置 HTTP 请求信息
func (e Entry) WithHTTP(method, path string, status int, durationMs int64, clientIP string) Entry {
	e.Method = method
	e.Path = path
	e.Status = status
	e.DurationMs = durationMs
	e.ClientIP = clientIP
	return e
}

// WithExtra 添加额外字段
func (e Entry) WithExtra(key string, value interface{}) Entry {
	if e.Extra == nil {
		e.Extra = make(map[string]interface{})
	}
	e.Extra[key] = value
	return e
}

// Time 返回时间对象
func (e Entry) Time() time.Time {
	return time.UnixMilli(e.Timestamp)
}

// IsExpired 判断日志是否已过期（超过保留天数）
func (e Entry) IsExpired() bool {
	cutoff := time.Now().AddDate(0, 0, -RetentionDays).UnixMilli()
	return e.Timestamp < cutoff
}

// generateID 生成日志条目唯一ID
func generateID(timestamp int64, category, action, message string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d|%s|%s|%s|%d", timestamp, category, action, message, time.Now().UnixNano())
	return hex.EncodeToString(h.Sum(nil))[:16]
}
