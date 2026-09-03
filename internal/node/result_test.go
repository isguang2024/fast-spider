package node

import (
	"bytes"
	"testing"
)

func TestCloudResultPagesStayWithinOneMiBAndPreserveSource(t *testing.T) {
	source := append(bytes.Repeat([]byte("x"), maxCloudResultPage), []byte("尾")...)
	pages := cloudResultPages(source)
	if len(pages) != 2 || !bytes.Equal(bytes.Join(pages, nil), source) {
		t.Fatalf("pages=%d source was not preserved", len(pages))
	}
	for i, page := range pages {
		if len(page) > maxCloudResultPage {
			t.Fatalf("page %d size=%d", i, len(page))
		}
	}
}
