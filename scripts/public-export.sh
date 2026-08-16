#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$ROOT"

output=""
revision="HEAD"
require_license=0
skip_tests=0

usage() {
  cat <<'EOF'
Usage: bash scripts/public-export.sh --output <new-directory> [options]

Options:
  --revision <ref>       Export one committed Git revision (default: HEAD).
  --require-license      Fail unless LICENSE or LICENSE.txt exists in the export.
  --skip-tests           Skip go mod verify and go test ./... in the exported tree.
  -h, --help             Show this help.

The source repository must be clean. The output directory must not already exist
and must be outside the source repository. The command creates a brand-new Git
repository with exactly one root commit; it never rewrites or pushes source history.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --output)
      [[ $# -ge 2 ]] || { echo "--output requires a value" >&2; exit 2; }
      output="$2"
      shift 2
      ;;
    --revision)
      [[ $# -ge 2 ]] || { echo "--revision requires a value" >&2; exit 2; }
      revision="$2"
      shift 2
      ;;
    --require-license)
      require_license=1
      shift
      ;;
    --skip-tests)
      skip_tests=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

[[ -n "$output" ]] || { echo "--output is required" >&2; exit 2; }

if [[ -n "$(git status --porcelain --untracked-files=normal)" ]]; then
  echo "source repository must be clean before public export" >&2
  exit 1
fi

source_commit="$(git rev-parse --verify "${revision}^{commit}")"

if command -v cygpath >/dev/null 2>&1 && [[ "$output" =~ ^[A-Za-z]:[\\/] ]]; then
  output="$(cygpath -u "$output")"
fi
if [[ "$output" != /* ]]; then
  output="$PWD/$output"
fi
output_parent="$(dirname "$output")"
output_name="$(basename "$output")"
mkdir -p "$output_parent"
output_parent="$(cd "$output_parent" && pwd -P)"
output="$output_parent/$output_name"

if [[ -e "$output" ]]; then
  echo "output already exists: $output" >&2
  exit 1
fi
case "$output/" in
  "$ROOT/"*)
    echo "output must be outside the source repository" >&2
    exit 1
    ;;
esac

mkdir "$output"
success=0
private_marker_args=()
private_marker_file="$ROOT/.local/public-private-markers.txt"
if [[ -e "$private_marker_file" ]]; then
  private_marker_args=(--markers "$private_marker_file")
fi
cleanup_on_exit() {
  if [[ "$success" != "1" ]]; then
    rm -rf "$output"
  fi
}
trap cleanup_on_exit EXIT

git archive --format=tar "$source_commit" | tar -xf - -C "$output"

for forbidden in .git .local .learnings; do
  if [[ -e "$output/$forbidden" ]]; then
    echo "public archive unexpectedly contains $forbidden" >&2
    exit 1
  fi
done

go run ./cmd/secretscan --tree "$output" "${private_marker_args[@]}"

license_state="present"
if [[ ! -f "$output/LICENSE" && ! -f "$output/LICENSE.txt" ]]; then
  license_state="missing"
  if [[ "$require_license" == "1" ]]; then
    echo "public release requires LICENSE or LICENSE.txt" >&2
    exit 1
  fi
  echo "WARNING: LICENSE is missing; choose a license before publishing this repository." >&2
fi

if [[ "$skip_tests" != "1" ]]; then
  (
    cd "$output"
    go mod verify
    go test ./... -count=1
  )
fi

(
  cd "$output"
  git init -q
  git checkout -q -b main
  git config user.name "Fast Spider Public Export"
  git config user.email "noreply@example.invalid"
  git add -A
  git commit -q -m "Initial public source snapshot"
  [[ "$(git rev-list --count HEAD)" == "1" ]]
  [[ -z "$(git status --porcelain --untracked-files=normal)" ]]
)

public_commit="$(git -C "$output" rev-parse HEAD)"
file_count="$(git -C "$output" ls-files | wc -l | tr -d '[:space:]')"
success=1
trap - EXIT

printf 'PASS: Fast Spider public export\n'
printf 'sourceRevision=%s\n' "$source_commit"
printf 'publicCommit=%s\n' "$public_commit"
printf 'files=%s\n' "$file_count"
printf 'license=%s\n' "$license_state"
printf 'output=%s\n' "$output"
