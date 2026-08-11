package routing

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestSchemaFingerprintFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string][]schemaColumn)
	}{
		{name: "unknown column", mutate: func(schema map[string][]schemaColumn) {
			schema["providers"] = append(schema["providers"], schemaColumn{Name: "unexpected", Type: "TEXT"})
		}},
		{name: "missing column", mutate: func(schema map[string][]schemaColumn) {
			schema["providers"] = schema["providers"][:len(schema["providers"])-1]
		}},
		{name: "abnormal type", mutate: func(schema map[string][]schemaColumn) {
			columns := schema["proxy_config"]
			columns[1].Type = "TEXT"
			schema["proxy_config"] = columns
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dbPath := createRoutingFixture(t, test.mutate, false)
			inspector := New(Config{DBPath: dbPath, SettingsPath: filepath.Join(t.TempDir(), "missing.json")})
			snapshot, err := inspector.InspectApp(context.Background(), "codex")
			if err != nil {
				t.Fatal(err)
			}
			if available, _ := snapshot["available"].(bool); available {
				t.Fatalf("unexpected available snapshot: %#v", snapshot)
			}
			if snapshot["reason"] != "unsupported_schema" {
				t.Fatalf("reason = %v", snapshot["reason"])
			}
			if snapshot["schemaFingerprint"] == SupportedSchemaFingerprint() {
				t.Fatal("unsupported schema must not have the supported fingerprint")
			}
		})
	}
}

func TestSupportedSchemaFingerprintAndRouteCacheTTL(t *testing.T) {
	dbPath := createRoutingFixture(t, nil, true)
	now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	inspector := New(Config{
		DBPath: dbPath, SettingsPath: filepath.Join(t.TempDir(), "missing.json"),
		RouteTTL: 1500 * time.Millisecond, Now: func() time.Time { return now },
	})

	first, err := inspector.InspectApp(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if first["schemaFingerprint"] != SupportedSchemaFingerprint() || first["routingMode"] != "direct" {
		t.Fatalf("unexpected first snapshot: %#v", first)
	}
	serialized := fmt.Sprint(first)
	for _, forbidden := range []string{"super-secret", "raw-meta-secret", "user:password", "/private", "settings_config"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("route snapshot leaked %q: %s", forbidden, serialized)
		}
	}
	providers, _ := first["providers"].([]map[string]any)
	if len(providers) != 1 || providers[0]["endpointHost"] != "example.test:8443" || providers[0]["credentialPresent"] != true {
		t.Fatalf("sanitized provider facts = %#v", providers)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE proxy_config SET enabled = 1, live_takeover_active = 1 WHERE app_type = 'codex'`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	cached, err := inspector.InspectApp(context.Background(), "codex")
	if err != nil || cached["routingMode"] != "direct" {
		t.Fatalf("route changed before TTL: %#v, %v", cached, err)
	}
	now = now.Add(2 * time.Second)
	refreshed, err := inspector.InspectApp(context.Background(), "codex")
	if err != nil || refreshed["routingMode"] != "cc_switch" {
		t.Fatalf("route did not change after TTL: %#v, %v", refreshed, err)
	}
}

func TestSanitizedProviderFactsNeverExposeRawSecrets(t *testing.T) {
	settings := DecodeJSON(`{"api_key":"super-secret","env":{"ANTHROPIC_MODEL":"mapped-model","ANTHROPIC_API_KEY":"hidden"}}`)
	if !CredentialPresent(settings) {
		t.Fatal("credential presence was not detected")
	}
	models := ExtractModels("claude", settings, map[string]any{})
	joined := fmt.Sprint(models)
	if strings.Contains(joined, "super-secret") || strings.Contains(joined, "hidden") {
		t.Fatalf("models leaked credential material: %s", joined)
	}
	if got := EndpointHost("https://user:password@example.test:8443/private?q=secret"); got != "example.test:8443" {
		t.Fatalf("endpoint host = %q", got)
	}
}

func createRoutingFixture(t *testing.T, mutate func(map[string][]schemaColumn), withProxy bool) string {
	t.Helper()
	schema := make(map[string][]schemaColumn, len(supportedSchema))
	for table, columns := range supportedSchema {
		schema[table] = append([]schemaColumn(nil), columns...)
	}
	if mutate != nil {
		mutate(schema)
	}
	dbPath := filepath.Join(t.TempDir(), "cc-switch.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range schemaTableOrder {
		columns := schema[table]
		definitions := make([]string, 0, len(columns)+1)
		primary := make([]string, 0)
		for _, column := range columns {
			definition := column.Name + " " + column.Type
			if column.NotNull != 0 {
				definition += " NOT NULL"
			}
			if column.PK != 0 {
				primary = append(primary, column.Name)
			}
			definitions = append(definitions, definition)
		}
		if len(primary) > 0 {
			definitions = append(definitions, "PRIMARY KEY ("+strings.Join(primary, ", ")+")")
		}
		if _, err := db.Exec("CREATE TABLE " + table + " (" + strings.Join(definitions, ", ") + ")"); err != nil {
			t.Fatalf("create %s: %v", table, err)
		}
	}
	if withProxy {
		insertSchemaRow(t, db, "proxy_config", schema["proxy_config"], map[string]any{
			"app_type": "codex", "listen_address": "127.0.0.1", "listen_port": 15731,
			"created_at": "2026-08-11T00:00:00Z", "updated_at": "2026-08-11T00:00:00Z",
		})
		insertSchemaRow(t, db, "providers", schema["providers"], map[string]any{
			"id": "provider-1", "app_type": "codex", "name": "Safe Provider", "is_current": 1,
			"settings_config": `{"api_key":"super-secret","config":"model = 'mapped-model'\nwire_api = 'responses'"}`,
			"meta":            `{"api_format":"openai_responses","secret":"raw-meta-secret"}`, "cost_multiplier": "1",
		})
		insertSchemaRow(t, db, "provider_endpoints", schema["provider_endpoints"], map[string]any{
			"id": 1, "provider_id": "provider-1", "app_type": "codex", "url": "https://user:password@example.test:8443/private?token=secret",
		})
	}
	return dbPath
}

func insertSchemaRow(t *testing.T, db *sql.DB, table string, columns []schemaColumn, overrides map[string]any) {
	t.Helper()
	names := make([]string, len(columns))
	marks := make([]string, len(columns))
	values := make([]any, len(columns))
	for index, column := range columns {
		names[index], marks[index] = column.Name, "?"
		if value, ok := overrides[column.Name]; ok {
			values[index] = value
			continue
		}
		switch column.Type {
		case "INTEGER", "BOOLEAN":
			values[index] = 0
		case "REAL":
			values[index] = 0.0
		default:
			values[index] = ""
		}
	}
	query := "INSERT INTO " + table + " (" + strings.Join(names, ",") + ") VALUES (" + strings.Join(marks, ",") + ")"
	if _, err := db.Exec(query, values...); err != nil {
		t.Fatal(err)
	}
}
