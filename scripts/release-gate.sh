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
full: core + repeated Node tests, short fuzzing where supported, race where
      supported, real Browser/Codex E2E, and the complete Local Bridge→Codex smoke.

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
format_output="$(gofmt -l "${go_files[@]}")"
if [[ -n "$format_output" ]]; then
  echo "gofmt is required for:" >&2
  printf '%s\n' "$format_output" >&2
  exit 1
fi

step "Git whitespace check" git diff --check
secret_matches="$(git grep -nE 'BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY|sk-[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9]{20,}' -- . ':!.learnings' || true)"
if [[ -n "$secret_matches" ]]; then
  echo "tracked files contain a likely private key or token pattern:" >&2
  printf '%s\n' "$secret_matches" >&2
  exit 1
fi
echo "==> Tracked secret pattern scan: PASS"
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
  step "Real Codex E2E" env FAST_SPIDER_CODEX_E2E=1 go test -tags codexe2e ./internal/agent -run TestCodexAdapterRealE2E -count=1
  step "Local Bridge to Codex product smoke" env FAST_SPIDER_CODEX_E2E=1 go test -tags producte2e ./internal/e2e -run TestLocalBridgeCodexProductE2E -count=1
fi

printf '\nPASS: Fast Spider %s release gate\n' "$mode"
