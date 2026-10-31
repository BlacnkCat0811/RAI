#!/usr/bin/env bash
# Install pinned Go and PostgreSQL toolchains under .local/toolchain.
# No sudo, no Homebrew and no Docker: everything stays inside the workspace.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
umask 022

go_archive="go$rai_go_version.darwin-amd64.tar.gz"
case "$(uname -m)" in
  arm64)
    go_archive="go$rai_go_version.darwin-arm64.tar.gz"
    go_sha256="dd3908cf20959d798c1e06ebe5b72213f0505ce01373b40ce3110975a6cd99c2"
    ;;
  x86_64)
    go_archive="go$rai_go_version.darwin-amd64.tar.gz"
    go_sha256="b6f8e931e4ab497fcde6da8ae4499a58f9d258e55c8a80824a516eae5950a750"
    ;;
  *) echo "unsupported macOS architecture: $(uname -m)" >&2; exit 1 ;;
esac
go_url="https://go.dev/dl/$go_archive"
pg_url="https://github.com/PostgresApp/PostgresApp/releases/download/v2.9.6/Postgres-2.9.6-18.dmg"
pg_sha256="9fc7d0dc08cf46dfd94bb32cbaaad81b41b37847a42d6dcb2f9fbd292813defb"

mkdir -p "$rai_toolchain" "$rai_local/downloads"

go_ready=false
if [[ -x "$rai_toolchain/go/bin/go" ]] && "$rai_toolchain/go/bin/go" version | grep -q "go$rai_go_version "; then
  go_ready=true
fi
if $go_ready; then
  echo "Go $rai_go_version already installed"
else
  if [[ ! -f "$rai_local/downloads/$go_archive" ]]; then
    echo "Downloading $go_url"
    curl -fsSL --retry 3 -o "$rai_local/downloads/$go_archive.part" "$go_url"
    mv "$rai_local/downloads/$go_archive.part" "$rai_local/downloads/$go_archive"
  fi
  actual="$(shasum -a 256 "$rai_local/downloads/$go_archive" | awk '{print $1}')"
  if [[ "$actual" != "$go_sha256" ]]; then
    echo "Go archive checksum mismatch" >&2
    exit 1
  fi
  rm -rf "$rai_toolchain/go.tmp"
  mkdir -p "$rai_toolchain/go.tmp"
  tar -xzf "$rai_local/downloads/$go_archive" -C "$rai_toolchain/go.tmp"
  rm -rf "$rai_toolchain/go"
  mv "$rai_toolchain/go.tmp/go" "$rai_toolchain/go"
  rmdir "$rai_toolchain/go.tmp"
  "$rai_toolchain/go/bin/go" version
fi

pg_ready=false
if [[ -x "$rai_pgbin/postgres" ]] && "$rai_pgbin/postgres" --version | grep -q " $rai_pg_version "; then
  pg_ready=true
fi
if $pg_ready; then
  echo "PostgreSQL $rai_pg_version already installed"
else
  dmg="$rai_local/downloads/Postgres-2.9.6-18.dmg"
  if [[ ! -f "$dmg" ]]; then
    echo "Downloading $pg_url"
    curl -fsSL --retry 3 -o "$dmg.part" "$pg_url"
    mv "$dmg.part" "$dmg"
  fi
  actual="$(shasum -a 256 "$dmg" | awk '{print $1}')"
  if [[ "$actual" != "$pg_sha256" ]]; then
    echo "PostgreSQL image checksum mismatch" >&2
    exit 1
  fi
  mountpoint="$(mktemp -d "${TMPDIR:-/tmp}/rai-pg-dmg.XXXXXX")"
  cleanup_mount() {
    if mount | grep -q "on $mountpoint "; then
      hdiutil detach "$mountpoint" -quiet || true
    fi
    rmdir "$mountpoint" 2>/dev/null || true
  }
  trap cleanup_mount EXIT
  hdiutil attach -nobrowse -readonly -mountpoint "$mountpoint" "$dmg" >/dev/null
  rm -rf "$rai_toolchain/Postgres.app"
  ditto "$mountpoint/Postgres.app" "$rai_toolchain/Postgres.app"
  cleanup_mount
  trap - EXIT
  "$rai_pgbin/postgres" --version
fi

echo "Toolchain ready under $rai_toolchain"
