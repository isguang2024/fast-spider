package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChatGPTRequestDefaultsValuesAndOmission(t *testing.T) {
	d := DefaultChatGPTAdvancedConfig().RequestDefaults
	d.ServiceTier = "priority"
	d.ConsumerLockdownModeDisabled = false
	d.ForceParallelSwitch = "on"
	for name, body := range map[string]map[string]any{
		"quick":    chatgptQuickChatBody("hello", "auto"),
		"complete": chatgptNewChatBodyWithThinking("hello", "auto", ""),
		"followup": chatgptFollowUpBodyWithThinking("conversation", "parent", "hello", "auto", ""),
	} {
		d.applyToBody(body, "")
		if body["service_tier"] != "priority" || body["consumer_lockdown_mode_disabled"] != false || body["force_parallel_switch"] != "on" {
			t.Fatalf("%s configured values not applied: %#v", name, body)
		}
		d.applyToBody(body, "fast")
		if body["service_tier"] != "fast" {
			t.Fatal("explicit tier not applied")
		}
		ChatGPTCloudRequestDefaults{}.applyToBody(body, "fast")
		for _, key := range []string{"service_tier", "consumer_lockdown_mode_disabled", "force_parallel_switch"} {
			if _, exists := body[key]; exists {
				t.Fatalf("%s disabled field %s sent", name, key)
			}
		}
	}
}

func TestChatGPTRequestDefaultsOldConfig(t *testing.T) {
	for _, raw := range []string{
		`{"version":1,"models":[]}`,
		`{"version":1,"models":[],"requestDefaults":{"enableServiceTier":true,"enableConsumerLockdownModeDisabled":true,"enableForceParallelSwitch":true}}`,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ChatGPTAdvancedConfigFileName), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadChatGPTAdvancedConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.RequestDefaults != DefaultChatGPTAdvancedConfig().RequestDefaults {
			t.Fatalf("old config defaults: %+v", cfg.RequestDefaults)
		}
	}
}
