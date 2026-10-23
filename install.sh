#!/bin/sh
set -eu
if [ "$#" -ne 2 ]; then
  echo 'Usage: install.sh <downloaded-binary> <SHA256SUMS>' >&2
  exit 2
fi
inbox_asset=$1
inbox_checksums=$2
inbox_name=$(basename "$inbox_asset")
inbox_expected=$(awk -v name="$inbox_name" '$2 == name {print $1}' "$inbox_checksums")
if [ -z "$inbox_expected" ]; then echo 'Asset is not listed in SHA256SUMS' >&2; exit 1; fi
if command -v sha256sum >/dev/null 2>&1; then
  inbox_actual=$(sha256sum "$inbox_asset" | awk '{print $1}')
else
  inbox_actual=$(shasum -a 256 "$inbox_asset" | awk '{print $1}')
fi
if [ "$inbox_expected" != "$inbox_actual" ]; then echo 'Checksum mismatch' >&2; exit 1; fi
inbox_install_dir=${INBOX_INSTALL_DIR:-"$HOME/.local/bin"}
mkdir -p "$inbox_install_dir"
if [ -e "$inbox_install_dir/wecom-inbox" ]; then
  echo 'Existing executable retained. Choose a new INBOX_INSTALL_DIR for this release.' >&2
  exit 1
fi
install -m 0755 "$inbox_asset" "$inbox_install_dir/wecom-inbox"
echo "Installed $inbox_install_dir/wecom-inbox"
