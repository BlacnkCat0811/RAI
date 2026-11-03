#!/usr/bin/env bash
# Install pinned Go, PostgreSQL, sqlc and Node toolchains under .local/toolchain
# and .local/bin. No sudo, no Homebrew and no Docker: everything stays inside the
# workspace.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
umask 022

go_archive="go$rai_go_version.darwin-amd64.tar.gz"
case "$(uname -m)" in
  arm64)
    go_archive="go$rai_go_version.darwin-arm64.tar.gz"
    go_sha256="dd3908cf20959d798c1e06ebe5b72213f0505ce01373b40ce3110975a6cd99c2"
    sqlc_archive="sqlc_${rai_sqlc_version}_darwin_arm64.tar.gz"
    sqlc_sha256="21602158c99eb1f2bae197a66abfb1941d1e9e50b23125bb193349c6b1acc71e"
    node_arch="arm64"
    node_sha256="61130f394c1630d211dd50aecc4353d379480f36d3ac913cd85dbba1aed585c6"
    ;;
  x86_64)
    go_archive="go$rai_go_version.darwin-amd64.tar.gz"
    go_sha256="b6f8e931e4ab497fcde6da8ae4499a58f9d258e55c8a80824a516eae5950a750"
    sqlc_archive="sqlc_${rai_sqlc_version}_darwin_amd64.tar.gz"
    sqlc_sha256="c5af76772e3785d21663a62697056b383f07629979b1bd25b93872e73dbd519b"
    node_arch="x64"
    node_sha256="58e99022c2ff89395576cc7fd4d98cea24bb68081475d5f88b801ee8729fb026"
    ;;
  *) echo "unsupported macOS architecture: $(uname -m)" >&2; exit 1 ;;
esac
go_url="https://go.dev/dl/$go_archive"
# sqlc publishes no upstream checksum file; these digests were computed from the
# HTTPS release assets and pinned here, so a changed download still fails loudly.
sqlc_url="https://github.com/sqlc-dev/sqlc/releases/download/v$rai_sqlc_version/$sqlc_archive"
# Node digests come from the upstream SHASUMS256.txt of the pinned release; the
# toolchain is only used by the separate OpenAPI contract lint.
node_archive="node-v$rai_node_version-darwin-$node_arch.tar.gz"
node_url="https://nodejs.org/dist/v$rai_node_version/$node_archive"
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

sqlc_ready=false
if [[ -x "$rai_sqlc" ]] && "$rai_sqlc" version | grep -qx "v$rai_sqlc_version"; then
  sqlc_ready=true
fi
if $sqlc_ready; then
  echo "sqlc $rai_sqlc_version already installed"
else
  if [[ ! -f "$rai_local/downloads/$sqlc_archive" ]]; then
    echo "Downloading $sqlc_url"
    curl -fsSL --retry 3 -o "$rai_local/downloads/$sqlc_archive.part" "$sqlc_url"
    mv "$rai_local/downloads/$sqlc_archive.part" "$rai_local/downloads/$sqlc_archive"
  fi
  actual="$(shasum -a 256 "$rai_local/downloads/$sqlc_archive" | awk '{print $1}')"
  if [[ "$actual" != "$sqlc_sha256" ]]; then
    echo "sqlc archive checksum mismatch" >&2
    exit 1
  fi
  mkdir -p "$rai_bin"
  tar -xzf "$rai_local/downloads/$sqlc_archive" -C "$rai_bin" sqlc
  chmod 0755 "$rai_sqlc"
  "$rai_sqlc" version
fi

node_ready=false
if [[ -x "$rai_node_bin/node" ]] && "$rai_node_bin/node" --version | grep -qx "v$rai_node_version"; then
  node_ready=true
fi
if $node_ready; then
  echo "Node $rai_node_version already installed"
else
  if [[ ! -f "$rai_local/downloads/$node_archive" ]]; then
    echo "Downloading $node_url"
    curl -fsSL --retry 3 -o "$rai_local/downloads/$node_archive.part" "$node_url"
    mv "$rai_local/downloads/$node_archive.part" "$rai_local/downloads/$node_archive"
  fi
  actual="$(shasum -a 256 "$rai_local/downloads/$node_archive" | awk '{print $1}')"
  if [[ "$actual" != "$node_sha256" ]]; then
    echo "Node archive checksum mismatch" >&2
    exit 1
  fi
  rm -rf "$rai_toolchain/node.tmp"
  mkdir -p "$rai_toolchain/node.tmp"
  tar -xzf "$rai_local/downloads/$node_archive" -C "$rai_toolchain/node.tmp"
  rm -rf "$rai_toolchain/node"
  mv "$rai_toolchain/node.tmp/node-v$rai_node_version-darwin-$node_arch" "$rai_toolchain/node"
  rmdir "$rai_toolchain/node.tmp"
  "$rai_node_bin/node" --version
fi

echo "Toolchain ready under $rai_toolchain; sqlc at $rai_sqlc; node at $rai_node_bin"
