package server

import "testing"

func TestAdaptRollingFileEditResultFrom20Node(t *testing.T) {
	result := map[string]any{
		"path": "C:/work/file.txt", "action": "replace", "beforeSha256": "sha256:old", "afterSha256": "sha256:new",
		"bytes": float64(42), "changed": true, "editCount": float64(2), "diff": "large legacy diff",
	}
	adaptRollingFileEditResult(result, "replace")
	if result["success"] != true || result["operation"] != "replace" || result["oldSha256"] != "sha256:old" || result["newSha256"] != "sha256:new" || result["editsApplied"] != int64(2) {
		t.Fatalf("adapted result=%#v", result)
	}
	if _, exists := result["afterSha256"]; exists {
		t.Fatalf("legacy fields remain: %#v", result)
	}
}

func TestAdaptRollingCodeSearchReasonFrom20Node(t *testing.T) {
	for legacy, stable := range map[string]string{
		"platform_unsupported": "RG_NOT_FOUND", "component_missing": "RG_NOT_FOUND", "component_invalid": "RG_NOT_FOUND",
		"start_failed": "RG_START_FAILED", "command_failed": "RG_EXIT_ERROR", "output_limit": "RG_OUTPUT_LIMIT", "output_invalid": "RG_OUTPUT_INVALID",
	} {
		result := map[string]any{"fallbackReason": legacy}
		adaptRollingCodeSearchResult(result)
		if result["fallbackReason"] != stable {
			t.Fatalf("%s => %v, want %s", legacy, result["fallbackReason"], stable)
		}
	}
}
