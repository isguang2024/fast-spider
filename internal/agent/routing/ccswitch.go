package routing

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func (i *Inspector) InspectApp(ctx context.Context, appType string) (map[string]any, error) {
	appType = strings.TrimSpace(appType)
	if appType != "claude" && appType != "codex" && appType != "claude-desktop" {
		return nil, fmt.Errorf("appType must be claude, codex, or claude-desktop")
	}
	if cached, ok := i.cached(appType); ok {
		return cached, nil
	}
	readCtx, cancel := context.WithTimeout(ctx, i.readTimeout)
	defer cancel()
	out, err := i.inspect(readCtx, appType)
	if err != nil {
		return nil, err
	}
	i.store(appType, out)
	return cloneMap(out), nil
}

func (i *Inspector) inspect(ctx context.Context, appType string) (map[string]any, error) {
	out := map[string]any{"appType": appType, "harness": HarnessName(appType), "source": "cc_switch_db", "authoritative": true, "capturedAt": i.now().UTC().Format(time.RFC3339Nano)}
	if info, err := os.Stat(i.dbPath); err != nil || !info.Mode().IsRegular() {
		out["available"] = false
		out["reason"] = "database_unavailable"
		return out, nil
	}
	settings := i.readDeviceSettings()
	if enabled, ok := settings["enableLocalProxy"].(bool); ok {
		out["localProxyEnabled"] = enabled
	}
	if current := CurrentProviderSetting(settings, appType); current != "" {
		out["deviceCurrentProviderId"] = current
	}
	dsn := "file:" + filepath.ToSlash(i.dbPath) + "?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(1000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open CC Switch database")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("read CC Switch database")
	}
	fingerprint, supported, schemaErr := inspectSchema(ctx, db)
	if fingerprint != "" {
		out["schemaFingerprint"] = fingerprint
	}
	if schemaErr != nil || !supported {
		out["available"] = false
		out["reason"] = "unsupported_schema"
		return out, nil
	}
	out["available"] = true
	proxy, err := inspectProxy(ctx, db, appType)
	if err != nil {
		return unsupportedSnapshot(out), nil
	}
	out["proxy"] = proxy
	mode := RoutingMode(proxy)
	out["routingMode"] = mode
	providers, current, err := inspectProviders(ctx, db, appType)
	if err != nil {
		return unsupportedSnapshot(out), nil
	}
	out["providers"] = providers
	if current != nil {
		out["currentProvider"] = current
		dbCurrent, _ := current["providerId"].(string)
		if dbCurrent != "" {
			out["dbCurrentProviderId"] = dbCurrent
			if deviceCurrent, _ := out["deviceCurrentProviderId"].(string); deviceCurrent != "" {
				out["selectionConsistent"] = deviceCurrent == dbCurrent
			}
		}
		out["effectiveCapabilities"] = EffectiveCapabilities(appType, mode, current)
	} else {
		out["effectiveCapabilities"] = EffectiveCapabilities(appType, mode, nil)
	}
	last, err := inspectLastRequest(ctx, db, appType)
	if err != nil {
		return unsupportedSnapshot(out), nil
	}
	if last != nil {
		out["lastRequest"] = last
	}
	return out, nil
}

func unsupportedSnapshot(out map[string]any) map[string]any {
	delete(out, "providers")
	delete(out, "currentProvider")
	delete(out, "proxy")
	out["available"] = false
	out["reason"] = "unsupported_schema"
	return out
}

func (i *Inspector) readDeviceSettings() map[string]any {
	raw, err := os.ReadFile(i.settingsPath)
	if err != nil || len(raw) > 256<<10 {
		return map[string]any{}
	}
	var settings map[string]any
	if json.Unmarshal(raw, &settings) != nil {
		return map[string]any{}
	}
	return settings
}

func HarnessName(appType string) string {
	switch appType {
	case "claude":
		return "claude_code"
	case "claude-desktop":
		return "claude_desktop"
	case "codex":
		return "codex"
	}
	return appType
}

func CurrentProviderSetting(settings map[string]any, appType string) string {
	key := ""
	switch appType {
	case "claude":
		key = "currentProviderClaude"
	case "claude-desktop":
		key = "currentProviderClaudeDesktop"
	case "codex":
		key = "currentProviderCodex"
	}
	value, _ := settings[key].(string)
	return strings.TrimSpace(value)
}

func inspectProxy(ctx context.Context, db *sql.DB, appType string) (map[string]any, error) {
	out := map[string]any{"takeoverEnabled": false, "liveTakeoverActive": false, "proxyEnabled": false}
	var proxyEnabled, takeoverEnabled, liveTakeoverActive, autoFailover int
	var address string
	var port int
	err := db.QueryRowContext(ctx, `SELECT proxy_enabled, enabled, live_takeover_active, listen_address, listen_port, auto_failover_enabled FROM proxy_config WHERE app_type = ?`, appType).Scan(&proxyEnabled, &takeoverEnabled, &liveTakeoverActive, &address, &port, &autoFailover)
	if err == sql.ErrNoRows {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	out["proxyEnabled"] = proxyEnabled != 0
	out["takeoverEnabled"] = takeoverEnabled != 0
	out["liveTakeoverActive"] = liveTakeoverActive != 0
	out["autoFailoverEnabled"] = autoFailover != 0
	out["listenAddress"] = address
	out["listenPort"] = port
	return out, nil
}

func inspectProviders(ctx context.Context, db *sql.DB, appType string) ([]map[string]any, map[string]any, error) {
	endpointByProvider := map[string]string{}
	rows, err := db.QueryContext(ctx, `SELECT provider_id, url FROM provider_endpoints WHERE app_type = ? ORDER BY id`, appType)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id, endpoint string
		if err := rows.Scan(&id, &endpoint); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if endpointByProvider[id] == "" {
			endpointByProvider[id] = endpoint
		}
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	healthByProvider := map[string]map[string]any{}
	rows, err = db.QueryContext(ctx, `SELECT provider_id, is_healthy, consecutive_failures, last_success_at, last_failure_at FROM provider_health WHERE app_type = ?`, appType)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id string
		var healthy, failures int
		var success, failure sql.NullString
		if err := rows.Scan(&id, &healthy, &failures, &success, &failure); err != nil {
			rows.Close()
			return nil, nil, err
		}
		h := map[string]any{"healthy": healthy != 0, "consecutiveFailures": failures}
		if success.Valid {
			h["lastSuccessAt"] = success.String
		}
		if failure.Valid {
			h["lastFailureAt"] = failure.String
		}
		healthByProvider[id] = h
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	rows, err = db.QueryContext(ctx, `SELECT id, name, settings_config, category, meta, is_current, in_failover_queue, provider_type FROM providers WHERE app_type = ? ORDER BY is_current DESC, sort_index ASC, name ASC LIMIT 128`, appType)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	providers := make([]map[string]any, 0)
	var current map[string]any
	for rows.Next() {
		var id, name, settingsRaw, metaRaw string
		var category, providerType sql.NullString
		var isCurrent, failover int
		if err := rows.Scan(&id, &name, &settingsRaw, &category, &metaRaw, &isCurrent, &failover, &providerType); err != nil {
			return nil, nil, err
		}
		settings, meta := DecodeJSON(settingsRaw), DecodeJSON(metaRaw)
		provider := map[string]any{"providerId": id, "name": name, "appType": appType, "isCurrent": isCurrent != 0, "inFailoverQueue": failover != 0, "credentialPresent": CredentialPresent(settings)}
		if category.Valid && category.String != "" {
			provider["category"] = category.String
		}
		if providerType.Valid && providerType.String != "" {
			provider["providerType"] = providerType.String
		}
		apiFormat := APIFormat(meta, settings)
		if apiFormat != "" {
			provider["apiFormat"] = apiFormat
		}
		if appType == "codex" {
			if config, ok := settings["config"].(string); ok {
				fields := ParseTopLevelConfig(config)
				for _, key := range []string{"model_provider", "wire_api", "service_tier"} {
					if value := fields[key]; value != "" {
						outputKey := map[string]string{"model_provider": "modelProvider", "wire_api": "wireAPI", "service_tier": "serviceTier"}[key]
						provider[outputKey] = value
					}
				}
			}
		}
		if host := EndpointHost(endpointByProvider[id]); host != "" {
			provider["endpointHost"] = host
		}
		if health := healthByProvider[id]; health != nil {
			provider["health"] = health
		}
		if models := ExtractModels(appType, settings, meta); len(models) > 0 {
			provider["models"] = models
		}
		if required, known := NeedsRouting(appType, apiFormat, meta); known {
			provider["needsLocalRouting"] = required
		}
		providers = append(providers, provider)
		if isCurrent != 0 {
			current = provider
		}
	}
	return providers, current, rows.Err()
}

func inspectLastRequest(ctx context.Context, db *sql.DB, appType string) (map[string]any, error) {
	var providerID, model string
	var requestModel, sessionID sql.NullString
	var createdAt int64
	err := db.QueryRowContext(ctx, `SELECT provider_id, model, request_model, session_id, created_at FROM proxy_request_logs WHERE app_type = ? ORDER BY created_at DESC LIMIT 1`, appType).Scan(&providerID, &model, &requestModel, &sessionID, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]any{"providerId": providerID, "upstreamModel": model, "createdAt": createdAt}
	if requestModel.Valid && requestModel.String != "" {
		out["requestModel"] = requestModel.String
	}
	if sessionID.Valid && sessionID.String != "" {
		out["sessionId"] = sessionID.String
	}
	return out, nil
}
