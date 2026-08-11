package agent

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/isguang2024/fast-spider/internal/agent/routing"
)

type CCSwitchInspector = routing.Inspector

func NewCCSwitchInspector(logger *slog.Logger) *CCSwitchInspector {
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".cc-switch")
	return routing.New(routing.Config{DBPath: filepath.Join(root, "cc-switch.db"), SettingsPath: filepath.Join(root, "settings.json"), Logger: logger})
}

func extractCCSwitchModels(appType string, settings, meta map[string]any) []map[string]any {
	return routing.ExtractModels(appType, settings, meta)
}
func parseCCSwitchTopLevelConfig(raw string) map[string]string {
	return routing.ParseTopLevelConfig(raw)
}
func ccSwitchNeedsRouting(appType, apiFormat string, meta map[string]any) (bool, bool) {
	return routing.NeedsRouting(appType, apiFormat, meta)
}
func ccSwitchCredentialPresent(settings map[string]any) bool {
	return routing.CredentialPresent(settings)
}
func ccSwitchEndpointHost(raw string) string             { return routing.EndpointHost(raw) }
func ccSwitchRoutingMode(proxy, _ map[string]any) string { return routing.RoutingMode(proxy) }
func deriveCCSwitchEffectiveCapabilities(appType string, routingMode any, provider map[string]any) map[string]any {
	mode, _ := routingMode.(string)
	return routing.EffectiveCapabilities(appType, mode, provider)
}
