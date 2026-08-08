package opsbackup

import "testing"

func FuzzValidateArchivePath(f *testing.F) {
	for _, seed := range []string{"hub.db", "artifacts/blobs/aa/blob", "../hub.db", "CON", `a\\b`, "a:b", "file."} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		validated, err := validateArchivePath(value)
		if err != nil {
			return
		}
		if validated != value {
			t.Fatalf("validator rewrote %q to %q", value, validated)
		}
		validatedAgain, err := validateArchivePath(validated)
		if err != nil || validatedAgain != validated {
			t.Fatalf("accepted path is not stable: %q -> %q err=%v", validated, validatedAgain, err)
		}
	})
}
