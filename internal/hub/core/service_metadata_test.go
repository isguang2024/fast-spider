package core

import "testing"

func TestAttachCapabilityCallMetadataPreservesJobOrigin(t *testing.T) {
	job := map[string]any{"jobId": "job_one", "requestId": "request_start", "traceId": "trace_start"}
	attachCapabilityCallMetadata(job, "request_watch", "trace_watch")
	if job["requestId"] != "request_start" || job["traceId"] != "trace_start" || job["callRequestId"] != "request_watch" || job["callTraceId"] != "trace_watch" {
		t.Fatalf("job metadata=%#v", job)
	}
	ordinary := map[string]any{}
	attachCapabilityCallMetadata(ordinary, "request_call", "trace_call")
	if ordinary["requestId"] != "request_call" || ordinary["traceId"] != "trace_call" {
		t.Fatalf("ordinary metadata=%#v", ordinary)
	}
}
