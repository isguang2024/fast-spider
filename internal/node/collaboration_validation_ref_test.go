package node

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestCollaborationValidationReceiptBindsNativeChildWithoutTopLevelThread(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(root, "mission.sqlite3")
	item := collaborationStateLocalItem("validation-child", "verifying")
	item["source_ref"], item["validation_owner"] = "test:returned", "validator"
	c := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node")})
	callCollaborationTest(t, c, "init", collaborationStateInitParams(db, []any{item}))
	bindTestDelivery(t, c, db)
	current := callCollaborationTest(t, c, "brief", collaborationStateIdentity(db, "delivery-1"))
	launch := "codex-agent:delivery-1#/root/validate_child"
	p := collaborationStateIdentity(db, "delivery-1")
	p["expectedRevision"], p["itemId"], p["launchRef"] = current["revision"], "validation-child", launch
	claim := callCollaborationTest(t, c, "validation_claim", p)
	receipt := collaborationStateIdentity(db, "delivery-1")
	receipt["expectedRevision"], receipt["itemId"], receipt["validationClaim"] = claim["revision"], "validation-child", claim["validationClaim"]
	for _, bad := range []string{"codex-agent:other-parent#/root/validate_child", "codex-agent:delivery-1#/root/other_child", " " + launch, launch + " ", "codex-agent:delivery-1#/root/../validate_child"} {
		receipt["executionRef"] = bad
		if _, err := c.collaborationControl(context.Background(), "validation_receipt", receipt); err == nil {
			t.Fatalf("accepted invalid child %q", bad)
		}
	}
	unchanged := callCollaborationTest(t, c, "brief", collaborationStateIdentity(db, "delivery-1"))
	if unchanged["revision"] != claim["revision"] {
		t.Fatal("invalid receipts changed the ledger")
	}
	receipt["executionRef"] = launch
	bound := callCollaborationTest(t, c, "validation_receipt", receipt)
	if bound["executionRef"] != launch {
		t.Fatal(bound)
	}
	replay := callCollaborationTest(t, c, "validation_receipt", receipt)
	if replay["replayed"] != true {
		t.Fatal(replay)
	}
	p = collaborationStateIdentity(db, "delivery-1")
	p["now"] = time.Now().Unix() + 700
	next := callCollaborationTest(t, c, "next_actions", p)
	found := false
	for _, a := range collaborationTestActions(next) {
		if a["kind"] == "notify_validation_due" {
			if a["nativeBindingLookup"] == nil {
				t.Fatalf("canonical child lacks recovery lookup: %#v", a)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("delivery has no child validation check")
	}
}
