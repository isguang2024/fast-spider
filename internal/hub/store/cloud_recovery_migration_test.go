package store

import (
	"database/sql"
	"os"
	"testing"
)

func TestCloudRecoveryMigrationPreservesLegacyReceipt(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA foreign_keys=ON", "CREATE TABLE owners(id TEXT PRIMARY KEY)", "CREATE TABLE cloud_collaborations(collaboration_id TEXT PRIMARY KEY)", "INSERT INTO owners VALUES('owner')", "INSERT INTO cloud_collaborations VALUES('collab')"} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"016_cloud_completion_notifications.sql", "017_cloud_completion_callback_types.sql"} {
		raw, err := os.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	insert := `INSERT INTO cloud_completion_notifications(notification_id,owner_id,collaboration_id,task_id,generation,notification_kind,outcome,source_session_id,target_session_id,state,claim_id,claimed_at,acked_at,created_at,updated_at,callback_type,result_text) VALUES(?,'owner','collab','task',1,?,'failed','chat','target','acked','claim',2,3,1,3,'text','')`
	if _, err = db.Exec(insert, "legacy", "completion"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("migrations/018_cloud_recovery_notifications.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	var state, kind, claim, text string
	var acked int64
	if err = db.QueryRow("SELECT state,notification_kind,claim_id,result_text,acked_at FROM cloud_completion_notifications WHERE notification_id='legacy'").Scan(&state, &kind, &claim, &text, &acked); err != nil {
		t.Fatal(err)
	}
	if state != "acked" || kind != "completion" || claim != "claim" || text != "" || acked != 3 {
		t.Fatal("legacy receipt changed")
	}
	if _, err = db.Exec(insert, "recovery", "recovery"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(insert, "other-result", "completion"); err == nil {
		t.Fatal("formal uniqueness lost")
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration broke foreign keys")
	}
}
