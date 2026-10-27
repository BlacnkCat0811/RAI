#!/usr/bin/env bash
# Run tests on the fnOS/N100 Docker host over SSH, keeping local CPU free.
# Every invocation gets its own remote directory, Docker network and PostgreSQL
# container, so concurrent runs do not share state. The remote `rai` database is
# never used.
set -euo pipefail
umask 077

host="${RAI_REMOTE_HOST:-rai-fnos}"
base="${RAI_REMOTE_BASE:-rai-remote-test}"
base="${base%/}"
run_id="$(date +%Y%m%d-%H%M%S)-$$"
root="$(cd "$(dirname "$0")/.." && pwd)"
remote_dir="$base/runs/$run_id"
case "$remote_dir" in
  "$base"/runs/*) ;;
  *) echo "Refusing unsafe remote path: $remote_dir" >&2; exit 2 ;;
esac
ssh_opts=(-T -o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=30 -o ServerAliveCountMax=3)
created=0

cleanup() {
  rc=$?
  trap - EXIT INT TERM
  if [[ "$created" == 1 ]]; then
    if [[ "${RAI_KEEP_REMOTE:-0}" == 1 ]]; then
      echo "Remote files kept: $host:$remote_dir" >&2
    else
      ssh "${ssh_opts[@]}" "$host" "rm -rf -- $(printf %q "$remote_dir")" >/dev/null 2>&1 || true
    fi
  fi
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if ! ssh "${ssh_opts[@]}" "$host" true; then
  echo "SSH key not accepted for $host; install this Mac's public key first:" >&2
  echo "Configure your SSH host alias and install its public key using ssh-copy-id." >&2
  exit 2
fi
if ! ssh "${ssh_opts[@]}" "$host" 'command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1'; then
  echo "Docker is not usable on $host (missing binary or permission denied)" >&2
  exit 2
fi

ssh "${ssh_opts[@]}" "$host" "mkdir -p -- $(printf %q "$remote_dir")"
created=1

if [[ "${RAI_USE_RSYNC:-1}" != 0 ]] && ssh "${ssh_opts[@]}" "$host" 'command -v rsync >/dev/null 2>&1'; then
  rsync -az --delete \
    --exclude=.git \
    --exclude=docs/evidence \
    --exclude='*.log' \
    "$root/" "$host:$remote_dir/"
else
  tar -C "$root" -czf - \
    --exclude=.git \
    --exclude=docs/evidence \
    --exclude='*.log' . | ssh "${ssh_opts[@]}" "$host" "tar -xzf - -C $(printf %q "$remote_dir")"
fi

remote_cmd="cd $(printf %q "$remote_dir") && bash scripts/remote-test.sh"
for arg in "$@"; do
  remote_cmd+=" $(printf %q "$arg")"
done

ssh "${ssh_opts[@]}" "$host" "$remote_cmd"
