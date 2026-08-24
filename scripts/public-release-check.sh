#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$ROOT"

require_file() {
  local path="$1"
  if [[ ! -f "$path" ]]; then
    echo "missing required public release file: $path" >&2
    exit 1
  fi
}

for path in \
  README.md \
  README.zh-CN.md \
  LICENSE \
  SECURITY.md \
  CONTRIBUTING.md \
  CODE_OF_CONDUCT.md \
  THIRD_PARTY_NOTICES.md \
  .env.example \
  .github/pull_request_template.md \
  .github/ISSUE_TEMPLATE/bug_report.md \
  .github/ISSUE_TEMPLATE/feature_request.md \
  docs/README.md \
  docs/getting-started.md \
  docs/getting-started.zh-CN.md \
  docs/free-local-deployment.md \
  docs/free-local-deployment.zh-CN.md \
  docs/security-model.md \
  docs/security-model.zh-CN.md \
  docs/npm-wrapper-plan.md \
  docs/configuration.md \
  docs/public-release.md \
  scripts/public-export.sh; do
  require_file "$path"
done

if git ls-files | grep -E '^(docs/progress/|\.local/|\.learnings/|data/|artifacts/|tmp/|dist/|bin/)' >/dev/null; then
  echo "tracked public source contains forbidden local/internal path" >&2
  git ls-files | grep -E '^(docs/progress/|\.local/|\.learnings/|data/|artifacts/|tmp/|dist/|bin/)' >&2
  exit 1
fi

filename_matches="$({
  git ls-files |
    grep -E '(^|/)(\.env($|\.)|id_rsa$|id_dsa$|id_ecdsa$|id_ed25519$|credentials\.json$|service-account\.json$|service_account\.json$|secrets\.(json|ya?ml)$|.*\.(pem|key|p12|pfx|jks|keystore|pkcs8|sqlite|sqlite3|db|log)$)' |
    grep -Ev '(^|/)\.env\.(example|sample|template)$' || true
})"
if [[ -n "$filename_matches" ]]; then
  echo "tracked public source contains sensitive-looking filenames" >&2
  printf '%s\n' "$filename_matches" >&2
  exit 1
fi

for pattern in \
  '/srv/backups' \
  'PRODUCTION READY' \
  'production deployment' \
  'Machine mach_'; do
  if git grep -n --fixed-strings "$pattern" -- ':!docs/public-release.md' ':!scripts/public-release-check.sh' >/tmp/fast-spider-public-check.$$ 2>/dev/null; then
    echo "public source contains internal release marker: $pattern" >&2
    cat /tmp/fast-spider-public-check.$$ >&2
    rm -f /tmp/fast-spider-public-check.$$
    exit 1
  fi
  rm -f /tmp/fast-spider-public-check.$$
done

go run ./cmd/secretscan

printf 'PASS: Fast Spider public release hygiene check\n'
