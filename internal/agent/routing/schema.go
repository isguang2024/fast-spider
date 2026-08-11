package routing

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

type schemaColumn struct {
	Name    string
	Type    string
	NotNull int
	PK      int
}

var supportedSchema = map[string][]schemaColumn{
	"providers": {
		{"id", "TEXT", 1, 1}, {"app_type", "TEXT", 1, 2}, {"name", "TEXT", 1, 0}, {"settings_config", "TEXT", 1, 0}, {"website_url", "TEXT", 0, 0}, {"category", "TEXT", 0, 0}, {"created_at", "INTEGER", 0, 0}, {"sort_index", "INTEGER", 0, 0}, {"notes", "TEXT", 0, 0}, {"icon", "TEXT", 0, 0}, {"icon_color", "TEXT", 0, 0}, {"meta", "TEXT", 1, 0}, {"is_current", "BOOLEAN", 1, 0}, {"in_failover_queue", "BOOLEAN", 1, 0}, {"cost_multiplier", "TEXT", 1, 0}, {"limit_daily_usd", "TEXT", 0, 0}, {"limit_monthly_usd", "TEXT", 0, 0}, {"provider_type", "TEXT", 0, 0},
	},
	"proxy_config": {
		{"app_type", "TEXT", 0, 1}, {"proxy_enabled", "INTEGER", 1, 0}, {"listen_address", "TEXT", 1, 0}, {"listen_port", "INTEGER", 1, 0}, {"enable_logging", "INTEGER", 1, 0}, {"enabled", "INTEGER", 1, 0}, {"auto_failover_enabled", "INTEGER", 1, 0}, {"max_retries", "INTEGER", 1, 0}, {"streaming_first_byte_timeout", "INTEGER", 1, 0}, {"streaming_idle_timeout", "INTEGER", 1, 0}, {"non_streaming_timeout", "INTEGER", 1, 0}, {"circuit_failure_threshold", "INTEGER", 1, 0}, {"circuit_success_threshold", "INTEGER", 1, 0}, {"circuit_timeout_seconds", "INTEGER", 1, 0}, {"circuit_error_rate_threshold", "REAL", 1, 0}, {"circuit_min_requests", "INTEGER", 1, 0}, {"default_cost_multiplier", "TEXT", 1, 0}, {"pricing_model_source", "TEXT", 1, 0}, {"live_takeover_active", "INTEGER", 1, 0}, {"created_at", "TEXT", 1, 0}, {"updated_at", "TEXT", 1, 0},
	},
	"provider_endpoints": {
		{"id", "INTEGER", 0, 1}, {"provider_id", "TEXT", 1, 0}, {"app_type", "TEXT", 1, 0}, {"url", "TEXT", 1, 0}, {"added_at", "INTEGER", 0, 0},
	},
	"provider_health": {
		{"provider_id", "TEXT", 1, 1}, {"app_type", "TEXT", 1, 2}, {"is_healthy", "INTEGER", 1, 0}, {"consecutive_failures", "INTEGER", 1, 0}, {"last_success_at", "TEXT", 0, 0}, {"last_failure_at", "TEXT", 0, 0}, {"last_error", "TEXT", 0, 0}, {"updated_at", "TEXT", 1, 0},
	},
	"proxy_request_logs": {
		{"request_id", "TEXT", 0, 1}, {"provider_id", "TEXT", 1, 0}, {"app_type", "TEXT", 1, 0}, {"model", "TEXT", 1, 0}, {"request_model", "TEXT", 0, 0}, {"input_tokens", "INTEGER", 1, 0}, {"output_tokens", "INTEGER", 1, 0}, {"cache_read_tokens", "INTEGER", 1, 0}, {"cache_creation_tokens", "INTEGER", 1, 0}, {"input_cost_usd", "TEXT", 1, 0}, {"output_cost_usd", "TEXT", 1, 0}, {"cache_read_cost_usd", "TEXT", 1, 0}, {"cache_creation_cost_usd", "TEXT", 1, 0}, {"total_cost_usd", "TEXT", 1, 0}, {"latency_ms", "INTEGER", 1, 0}, {"first_token_ms", "INTEGER", 0, 0}, {"duration_ms", "INTEGER", 0, 0}, {"status_code", "INTEGER", 1, 0}, {"error_message", "TEXT", 0, 0}, {"session_id", "TEXT", 0, 0}, {"provider_type", "TEXT", 0, 0}, {"is_streaming", "INTEGER", 1, 0}, {"cost_multiplier", "TEXT", 1, 0}, {"created_at", "INTEGER", 1, 0}, {"data_source", "TEXT", 1, 0}, {"pricing_model", "TEXT", 0, 0}, {"input_token_semantics", "INTEGER", 1, 0},
	},
}

var schemaTableOrder = []string{"providers", "proxy_config", "provider_endpoints", "provider_health", "proxy_request_logs"}

func SupportedSchemaFingerprint() string { return fingerprintSchema(supportedSchema) }

func inspectSchema(ctx context.Context, db *sql.DB) (string, bool, error) {
	actual := make(map[string][]schemaColumn, len(schemaTableOrder))
	for _, table := range schemaTableOrder {
		rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
		if err != nil {
			return "", false, err
		}
		columns := make([]schemaColumn, 0)
		for rows.Next() {
			var cid, notNull, pk int
			var name, columnType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
				rows.Close()
				return "", false, err
			}
			columns = append(columns, schemaColumn{Name: name, Type: strings.ToUpper(columnType), NotNull: notNull, PK: pk})
		}
		if err := rows.Close(); err != nil {
			return "", false, err
		}
		actual[table] = columns
	}
	fingerprint := fingerprintSchema(actual)
	return fingerprint, fingerprint == SupportedSchemaFingerprint(), nil
}

func fingerprintSchema(schema map[string][]schemaColumn) string {
	hash := sha256.New()
	for _, table := range schemaTableOrder {
		fmt.Fprintf(hash, "%s\n", table)
		for _, column := range schema[table] {
			fmt.Fprintf(hash, "%s|%s|%d|%d\n", column.Name, strings.ToUpper(column.Type), column.NotNull, column.PK)
		}
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}
