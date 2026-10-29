"""Calendar policy shared by monthly backup check and the explicit P7 adapter."""
import datetime as dt
from zoneinfo import ZoneInfo

ZONE = "Asia/Shanghai"
GRACE = dt.timedelta(hours=24)
UTC = dt.timezone.utc


def timestamp(value):
    if not isinstance(value, str):
        raise ValueError("invalid_backup_timestamp")
    result = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    if result.tzinfo is None:
        raise ValueError("timestamp_requires_timezone")
    return result


def schedule(now):
    if now.tzinfo is None:
        raise ValueError("timestamp_requires_timezone")
    local = now.astimezone(ZoneInfo(ZONE))
    current = local.replace(day=1, hour=3, minute=0, second=0, microsecond=0)
    previous = (current - dt.timedelta(days=1)).replace(day=1)
    following = (current.replace(day=28) + dt.timedelta(days=4)).replace(day=1)
    required = current if local >= current + GRACE else previous
    return {"timezone": ZONE, "required_snapshot_at": required.isoformat(),
            "latest_scheduled_at": (current if local >= current else previous).isoformat(),
            "grace_deadline": (current + GRACE).isoformat(),
            "next_scheduled_at": (current if local < current else following).isoformat()}


def health(inventory, now, last_attempt=None):
    result = schedule(now)
    result["checked_at"] = now.astimezone(UTC).isoformat()
    backups = inventory.get("backups", [])
    status = "BACKUP_OK"
    if inventory.get("invalid"):
        status = "BACKUP_CORRUPTED"
    elif not backups:
        status = "NO_BACKUP"
    else:
        # Publication alone is insufficient: a late upload of an old dump
        # must not satisfy this month's recovery point.
        latest = max(timestamp(m["snapshot_started_at"]) for m in backups)
        result["latest_snapshot_at"] = latest.isoformat()
        if latest > now + dt.timedelta(seconds=60):
            status = "BACKUP_CORRUPTED"
        elif latest < timestamp(result["required_snapshot_at"]):
            status = "BACKUP_OVERDUE"
    if (last_attempt and last_attempt["status"] == "BACKUP_FAILED" and status != "BACKUP_CORRUPTED"
            and (not backups or timestamp(last_attempt["time"]) >= latest)):
        status = "BACKUP_FAILED"
    result["status"] = status
    return result
