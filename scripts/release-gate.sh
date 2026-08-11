#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

mode="core"
case "${1:-}" in
  "") ;;
  --full) mode="full" ;;
  -h|--help)
    cat <<'EOF'
Usage: bash scripts/release-gate.sh [--full]

core: formatting, module integrity, vet, all unit/integration tests,
      current + Windows/Linux builds, Hub restore E2E, Local Bridge E2E.
full: core + explicit 0.4.2 Task Workspace/Search/file_read/file_edit/update/
      reconnect gates, the 0.4.3 consumed-current staging cleanup gate,
      the 0.4.4 legacy install artifacts cleanup gate, repeated Node tests,
      the 0.4.5 release backup prune gate, the 0.4.6 release staging prune gate,
      short fuzzing/race where supported,
      real Browser/CC Switch/Claude Code/Codex E2E, multi-provider Local Bridge
      discovery, and the complete Local Bridge→Codex smoke.

The script never installs tools, downloads update payloads, or modifies Git state.
EOF
    exit 0
    ;;
  *)
    echo "unknown argument: $1" >&2
    exit 2
    ;;
esac

step() {
  printf '\n==> %s\n' "$1"
  shift
  "$@"
}

printf 'Fast Spider release gate: %s\n' "$mode"
printf 'Go: %s\n' "$(go version)"

mapfile -d '' go_files < <(find cmd internal -type f -name '*.go' -print0)
public_source_files=()
while IFS= read -r -d '' file; do
  [[ -f "$file" ]] && public_source_files+=("$file")
done < <(git ls-files -co --exclude-standard -z -- . ':!.learnings/**')
format_output="$(gofmt -l "${go_files[@]}")"
if [[ -n "$format_output" ]]; then
  echo "gofmt is required for:" >&2
  printf '%s\n' "$format_output" >&2
  exit 1
fi

step "Git whitespace check" git diff --check
secret_matches="$(grep -nE 'BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY|sk-[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9]{20,}' -- "${public_source_files[@]}" || true)"
if [[ -n "$secret_matches" ]]; then
  echo "public-source files contain a likely private key or token pattern:" >&2
  printf '%s\n' "$secret_matches" >&2
  exit 1
fi
echo "==> Public-source secret pattern scan: PASS"
private_marker_matches="$(grep -nE 'mach_[A-Za-z0-9_-]{24,}|[A-Za-z]:\\\\repos\\\\GitHub' -- "${public_source_files[@]}" || true)"
if [[ -n "$private_marker_matches" ]]; then
  echo "public-source files contain a machine identifier or local repository path:" >&2
  printf '%s\n' "$private_marker_matches" >&2
  exit 1
fi
private_marker_file="$ROOT/.local/public-private-markers.txt"
if [[ -f "$private_marker_file" ]]; then
  while IFS= read -r marker || [[ -n "$marker" ]]; do
    marker="${marker%$'\r'}"
    [[ -z "$marker" || "$marker" == \#* ]] && continue
    marker_matches="$(grep -nFi -- "$marker" "${public_source_files[@]}" || true)"
    if [[ -n "$marker_matches" ]]; then
      echo "public-source files contain a locally configured private marker:" >&2
      printf '%s\n' "$marker_matches" >&2
      exit 1
    fi
  done < "$private_marker_file"
fi
echo "==> Public-source private marker scan: PASS"
step "Public export script syntax" bash -n scripts/public-export.sh
step "Module checksum verification" go mod verify
step "go.mod/go.sum tidiness" go mod tidy -diff
step "Static analysis" go vet ./...
step "All tests" go test ./... -count=1
step "Current-platform build" go build ./...
step "Windows amd64 build" env CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
step "Linux amd64 build" env CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...
step "Restored Hub health E2E" go test -tags opse2e ./internal/opsbackup -run TestRestoredHubStartsHealthy -count=1
step "Local Bridge E2E" go test -tags localbridgee2e ./internal/localbridge -count=1

if [[ "$mode" == "full" ]]; then
  step "0.4.2 Task Workspace gate" go test ./internal/node ./internal/nodeui -run 'Test(WorkingPlan|WorkingMarkdown|WorkingProgress)' -count=1
  step "0.4.2 Managed ripgrep/native search gate" go test ./internal/node ./internal/nodeui -run 'Test(ManagedRipgrep|NativeSearch|RipgrepJSON|SearchFileSelfTest)' -count=1
  step "0.4.2 ripgrep component packager gate" go test ./cmd/ripgreppack -count=1
  step "0.4.2 file_read gate" go test ./internal/node ./internal/protocol/v1 ./internal/hub/server -run 'Test(FileReadV2|FileReadCapability|MachineBoundaryEndToEnd)' -count=1
  step "0.4.2 file_edit gate" go test ./internal/node ./internal/protocol/v1 ./internal/hub/core ./internal/hub/server -run 'Test(FileEdit|FileWrite|MachineBoundaryEndToEnd)' -count=1
  step "0.4.2 updater temp E2E" go test ./internal/nodeupdate -run 'Test(CheckAndStageSignedNodeUpdate|CleanupStaleNodeUpdates)' -count=1
  step "0.4.3 updater consumed staging gate" go test ./internal/nodeupdate ./internal/nodeui -run 'Test(CleanupConsumedCurrent|StartupUpdateMaintenance)' -count=1
  step "0.4.4 legacy install artifacts cleanup gate" go test ./internal/nodeupdate ./internal/nodeui -run 'Test(LegacyInstallArtifact|LegacyCleanup|CleanupLegacyInstallArtifacts)' -count=1
  step "0.4.5 release backup prune gate" go test ./internal/opsbackup ./cmd/spiderctl -run 'Test(ReleaseBackupName|PruneReleaseBackups|BackupPruneCLI)' -count=1
  step "0.4.6 release staging prune gate" go test ./internal/opsbackup ./cmd/spiderctl -run 'Test(StagingPrune|PruneReleaseStaging)' -count=1
  step "0.4.2 reconnect temp E2E" go test ./internal/node -run 'Test(NodeReconnectAfterRevokedMachineCreatesFreshMachineIdentity|ReconnectBackoffsResetAfterStableSession)' -count=1
  step "Repeated Node regression" go test ./internal/node -count=3

  goarch="$(go env GOARCH)"
  goos="$(go env GOOS)"
  cgo="$(go env CGO_ENABLED)"
  if [[ "$goarch" == "386" ]]; then
    echo "==> Random fuzz: SKIP ($goos/$goarch toolchain; Fuzz seeds already ran in go test ./...)"
  else
    step "Security decoder fuzz" go test ./internal/security -run '^$' -fuzz FuzzEd25519EncodingDecoders -fuzztime=2s
    step "Backup path fuzz" go test ./internal/opsbackup -run '^$' -fuzz FuzzValidateArchivePath -fuzztime=2s
    step "Git input fuzz" go test ./internal/node -run '^$' -fuzz FuzzGitRefAndPathValidation -fuzztime=2s
  fi

  if [[ "$goarch" == "amd64" && "$cgo" == "1" ]]; then
    step "Race detector" go test -race ./...
  else
    echo "==> Race detector: SKIP (requires amd64 + CGO; current $goos/$goarch CGO_ENABLED=$cgo)"
  fi

  step "Real Browser E2E" env FAST_SPIDER_BROWSER_E2E=1 go test ./internal/node -run TestBrowserRealSidecarE2E -count=1
  step "Real CC Switch routing E2E" go test -tags codexe2e ./internal/agent -run TestCCSwitchInspectorRealE2E -count=1
  step "Real Claude Code E2E" env FAST_SPIDER_CLAUDE_E2E=1 go test -tags codexe2e ./internal/agent -run TestClaudeCodeAdapterRealE2E -count=1
  step "Real Codex E2E" env FAST_SPIDER_CODEX_E2E=1 go test -tags codexe2e ./internal/agent -run TestCodexAdapterRealE2E -count=1
  step "Local Bridge multi-provider discovery" go test -tags producte2e ./internal/e2e -run TestLocalBridgeProviderDiscoveryE2E -count=1
  step "Local Bridge to Codex product smoke" env FAST_SPIDER_CODEX_E2E=1 go test -tags producte2e ./internal/e2e -run TestLocalBridgeCodexProductE2E -count=1
fi

printf '\nPASS: Fast Spider %s release gate\n' "$mode"
