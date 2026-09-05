#!/bin/sh
# Seven verified daily slots. Manual/release backups are kept separately.
set -eu
umask 077
backup_dir=/var/lib/fast-spider-daily-backups
slot="$backup_dir/day-$(date -u +%u).zip"
staging=$(mktemp -d "$backup_dir/.pending.XXXXXX")
trap 'rm -f -- "$staging/backup.zip"; rmdir -- "$staging"' EXIT
/usr/local/bin/spiderctl backup --data-dir /var/lib/fast-spider --out "$staging/backup.zip"
/usr/local/bin/spiderctl backup-verify --file "$staging/backup.zip"
# Publish only after verification, retaining the previous slot on any failure.
mv -f -- "$staging/backup.zip" "$slot"
printf 'Verified daily backup: %s\n' "$slot"
