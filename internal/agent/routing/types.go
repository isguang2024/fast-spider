package routing

import (
	"log/slog"
	"sync"
	"time"
)

const (
	DefaultRouteTTL    = 1500 * time.Millisecond
	DefaultReadTimeout = 2 * time.Second
	maxRouteCacheKeys  = 8
)

type Config struct {
	DBPath       string
	SettingsPath string
	Logger       *slog.Logger
	RouteTTL     time.Duration
	ReadTimeout  time.Duration
	Now          func() time.Time
}

type Inspector struct {
	logger       *slog.Logger
	dbPath       string
	settingsPath string
	routeTTL     time.Duration
	readTimeout  time.Duration
	now          func() time.Time

	cacheMu sync.Mutex
	cache   map[string]routeCacheEntry
}

type routeCacheEntry struct {
	expires time.Time
	value   map[string]any
}

func New(config Config) *Inspector {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.RouteTTL <= 0 {
		config.RouteTTL = DefaultRouteTTL
	}
	if config.ReadTimeout <= 0 {
		config.ReadTimeout = DefaultReadTimeout
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Inspector{
		logger: config.Logger, dbPath: config.DBPath, settingsPath: config.SettingsPath,
		routeTTL: config.RouteTTL, readTimeout: config.ReadTimeout, now: config.Now,
		cache: make(map[string]routeCacheEntry),
	}
}

func (i *Inspector) cached(appType string) (map[string]any, bool) {
	i.cacheMu.Lock()
	defer i.cacheMu.Unlock()
	entry, ok := i.cache[appType]
	if !ok || !i.now().Before(entry.expires) {
		delete(i.cache, appType)
		return nil, false
	}
	return cloneMap(entry.value), true
}

func (i *Inspector) store(appType string, value map[string]any) {
	i.cacheMu.Lock()
	defer i.cacheMu.Unlock()
	if len(i.cache) >= maxRouteCacheKeys {
		var oldestKey string
		var oldest time.Time
		for key, entry := range i.cache {
			if oldestKey == "" || entry.expires.Before(oldest) {
				oldestKey, oldest = key, entry.expires
			}
		}
		delete(i.cache, oldestKey)
	}
	i.cache[appType] = routeCacheEntry{expires: i.now().Add(i.routeTTL), value: cloneMap(value)}
}

func (i *Inspector) Invalidate() {
	i.cacheMu.Lock()
	i.cache = make(map[string]routeCacheEntry)
	i.cacheMu.Unlock()
}

func (i *Inspector) DBPath() string { return i.dbPath }

func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	copyValue := make(map[string]any, len(value))
	for key, item := range value {
		switch typed := item.(type) {
		case map[string]any:
			copyValue[key] = cloneMap(typed)
		case []map[string]any:
			items := make([]map[string]any, len(typed))
			for index := range typed {
				items[index] = cloneMap(typed[index])
			}
			copyValue[key] = items
		case []any:
			items := make([]any, len(typed))
			copy(items, typed)
			copyValue[key] = items
		default:
			copyValue[key] = item
		}
	}
	return copyValue
}
