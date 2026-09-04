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

core: whitespace and worktree/index secret checks, plus Go formatting, module, vet, and
      unit/integration tests only when Go sources or module files changed.
full: core + only the Hub restore, Local Bridge, and real runtime E2E groups affected by
      the current diff. Release artifact builds happen once in the release build step.

Set FAST_SPIDER_GATE_ALL_E2E=1 to force every real runtime E2E group. The affected-file
set includes worktree/index changes and commits ahead of the configured upstream branch.

The script never installs tools, downloads update payloads, or modifies Git state.
EOF
    exit 0
    ;;
  *)
    echo "unknown argument: $1" >&2
    exit 2
    ;;
esac

private_marker_args=()
private_marker_file="$ROOT/.local/public-private-markers.txt"
if [[ -e "$private_marker_file" ]]; then
  private_marker_args=(--markers "$private_marker_file")
fi

step() {
  printf '\n==> %s\n' "$1"
  shift
  "$@"
}

release_changed_files() {
	local tracked_changes upstream base
	tracked_changes="$({
		git diff --name-only
		git diff --cached --name-only
		upstream="$(git rev-parse --abbrev-ref --symbolic-full-name '@{upstream}' 2>/dev/null || true)"
		if [[ -n "$upstream" ]]; then
			base="$(git merge-base HEAD "$upstream")"
			git diff --name-only "$base"...HEAD
		fi
	} | sed '/^$/d' | sort -u)"

	if [[ -z "$tracked_changes" ]] && git rev-parse --verify HEAD^ >/dev/null 2>&1; then
		tracked_changes="$(git diff --name-only HEAD^ HEAD)"
	fi

	{
		printf '%s\n' "$tracked_changes"
		git ls-files --others --exclude-standard
	} | sed '/^$/d' | sort -u
}

changed_files="$(release_changed_files)"
gate_all_e2e="${FAST_SPIDER_GATE_ALL_E2E:-0}"

should_run_e2e() {
	local pattern="$1"
	[[ "$gate_all_e2e" == "1" ]] || grep -Eq "$pattern" <<<"$changed_files"
}

has_changed() {
	local pattern="$1"
	grep -Eq "$pattern" <<<"$changed_files"
}

skip_check() {
	printf '==> %s: SKIP (no affected files)\n' "$1"
}

printf 'Fast Spider release gate: %s\n' "$mode"
printf 'Go: %s\n' "$(go version)"

step "Git whitespace check" git diff --check
step "Worktree and index secret scan" go run ./cmd/secretscan "${private_marker_args[@]}"

if has_changed '^scripts/public-export\.sh$'; then
	step "Public export script syntax" bash -n scripts/public-export.sh
else
	skip_check "Public export script syntax"
fi

if has_changed '(^|/).*\.go$|^go\.(mod|sum)$'; then
	mapfile -d '' go_files < <(find cmd internal -type f -name '*.go' -print0)
	format_output="$(gofmt -l "${go_files[@]}")"
	if [[ -n "$format_output" ]]; then
		echo "gofmt is required for:" >&2
		printf '%s\n' "$format_output" >&2
		exit 1
	fi
	if has_changed '^go\.(mod|sum)$'; then
		step "Module checksum verification" go mod verify
	else
		skip_check "Module checksum verification"
	fi
	step "go.mod/go.sum tidiness" go mod tidy -diff
	step "Static analysis" go vet ./...
	step "All tests" go test ./... -count=1
else
	skip_check "Go formatting and module checks"
	skip_check "Static analysis"
	skip_check "All tests"
fi

if [[ "$mode" == "full" ]]; then
	if should_run_e2e '^cmd/hub/|^internal/(hub|opsbackup|version)/|^go\.(mod|sum)$'; then
		step "Restored Hub health E2E" go test -tags opse2e ./internal/opsbackup -run TestRestoredHubStartsHealthy -count=1
	else
		skip_check "Restored Hub health E2E"
	fi

	if should_run_e2e '^cmd/node/|^internal/(agent|localbridge|nodeui|protocol)/|^go\.(mod|sum)$'; then
		step "Local Bridge E2E" go test -tags localbridgee2e ./internal/localbridge -count=1
	else
		skip_check "Local Bridge E2E"
	fi

	if should_run_e2e '^internal/node/wsl_|^internal/node/(shell|job)'; then
		if [[ "$(go env GOOS)" == "windows" ]]; then
			wsl_distribution="${FAST_SPIDER_WSL_DISTRIBUTION:-Ubuntu-24.04}"
				wsl_test_cwd="${FAST_SPIDER_WSL_TEST_CWD:-V:/tmp/fast-spider-wsl-gate}"
				step "Real WSL runtime E2E" env FAST_SPIDER_WSL_E2E=1 FAST_SPIDER_WSL_DISTRIBUTION="$wsl_distribution" FAST_SPIDER_WSL_TEST_CWD="$wsl_test_cwd" go test ./internal/node -run TestRealWSLExecutionRuntimeAndTiming -count=1
			else
				skip_check "Real WSL runtime E2E"
			fi
		else
			skip_check "Real WSL runtime E2E"
		fi

	if should_run_e2e '^cmd/browserpack/|^sidecar/browser/|^internal/componentmgr/|^internal/node/browser'; then
		if [[ "$(go env GOOS)" == "windows" ]]; then
			browser_gate_dir="$(mktemp -d "${TMPDIR:-/tmp}/fast-spider-browser-gate.XXXXXX")"
			trap 'rm -rf -- "$browser_gate_dir"' EXIT
			browser_component_version="$(node -p "require('./sidecar/browser/package.json').fastSpider.componentVersion || ''")"
			[[ "$browser_component_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "invalid Browser componentVersion: $browser_component_version" >&2; exit 1; }
			browser_executable_win="$(cd sidecar/browser && node -e "const { chromium } = require('playwright'); process.stdout.write(chromium.executablePath())")"
			browser_executable="$(cygpath -u "$browser_executable_win")"
			browser_cache="$(dirname "$(dirname "$(dirname "$browser_executable")")")"
			node_executable_win="$(node -p 'process.execPath')"
			step "Browser component package" go run ./cmd/browserpack --sidecar-dir sidecar/browser --node-exe "$node_executable_win" --browsers-dir "$browser_cache" --out "$browser_gate_dir/component.zip"
			step "Browser component extract" unzip -q "$browser_gate_dir/component.zip" -d "$browser_gate_dir/installed"
			step "Real packaged Browser E2E" env FAST_SPIDER_BROWSER_E2E=1 FAST_SPIDER_BROWSER_SIDECAR_DIR="$browser_gate_dir/installed" go test ./internal/node -run TestBrowserRealSidecarE2E -count=1
			rm -rf -- "$browser_gate_dir"
			trap - EXIT
			else
				step "Real Browser E2E" env FAST_SPIDER_BROWSER_E2E=1 go test ./internal/node -run TestBrowserRealSidecarE2E -count=1
			fi
		else
			skip_check "Real Browser E2E"
		fi

		if should_run_e2e '^internal/agent/routing/ccswitch\.go$|^internal/agent/ccswitch'; then
			step "Real CC Switch routing E2E" go test -tags codexe2e ./internal/agent -run TestCCSwitchInspectorRealE2E -count=1
		else
			skip_check "Real CC Switch routing E2E"
		fi

		if should_run_e2e '^internal/agent/claude'; then
			step "Real Claude Code E2E" env FAST_SPIDER_CLAUDE_E2E=1 go test -tags codexe2e ./internal/agent -run TestClaudeCodeAdapterRealE2E -count=1
		else
			skip_check "Real Claude Code E2E"
		fi

		if should_run_e2e '^internal/e2e/|^internal/localbridge/|^internal/agent/(provider|readiness)'; then
			step "Local Bridge multi-provider discovery" go test -tags producte2e ./internal/e2e -run TestLocalBridgeProviderDiscoveryE2E -count=1
		else
			skip_check "Local Bridge multi-provider discovery"
		fi

		if should_run_e2e '^cmd/node/|^internal/e2e/|^internal/localbridge/|^internal/agent/(codex|manager|provider|readiness|session_callback)'; then
			step "Real Local Bridge to Codex product E2E" env NO_PROXY="${NO_PROXY:+$NO_PROXY,}127.0.0.1,localhost" no_proxy="${no_proxy:+$no_proxy,}127.0.0.1,localhost" FAST_SPIDER_CODEX_E2E=1 go test -tags producte2e ./internal/e2e -run TestLocalBridgeCodexProductE2E -count=1
		else
			skip_check "Real Local Bridge to Codex product E2E"
		fi
fi

printf '\nPASS: Fast Spider %s release gate\n' "$mode"
