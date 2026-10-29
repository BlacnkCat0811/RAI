#!/usr/bin/env bash
# One cron entry only. No scheduler or host timezone is installed/changed here.
set -euo pipefail
umask 077
[[ $# == 1 && "$1" == /* ]] || { echo 'usage: rai-backup-scheduled.sh PRIVATE_ABSOLUTE_ENV_FILE' >&2; exit 2; }
root="$(cd "$(dirname "$0")/.." && pwd)"
python3 - "$1" <<'PY'
import os,pathlib,stat,sys
p=pathlib.Path(sys.argv[1]);s=p.lstat()
assert stat.S_ISREG(s.st_mode) and s.st_uid==os.getuid() and not s.st_mode&0o077 and s.st_nlink==1,'unsafe configuration file'
PY
set -a
source "$1"
set +a
# Cron's calendar uses the daemon/host timezone. TZ in a crontab normally only
# affects children. Refuse a wrong calendar window instead of silently running
# at a different hour. Installation must verify host zone == configured zone.
python3 - <<'PY'
import datetime,os,sys
from zoneinfo import ZoneInfo
now=datetime.datetime.now(ZoneInfo(os.environ['RAI_BACKUP_TIMEZONE']))
if (now.day,now.hour,now.minute)!=(1,3,0):
    print('MONTHLY_BACKUP_SKIPPED=outside_configured_monthly_window; check cron timezone or missed schedule',file=sys.stderr)
    sys.exit(75)
PY
exec "$root/scripts/rai-backup.sh" backup
