#!/usr/bin/env bash
# Build and package one plugin release: dist/<id>_<version>_linux_<arch>.tar.gz plus a .sha256 next to each.
# The archive root holds only the executable and README.md, LICENSE and NOTICE (the host installer rejects anything else).
# CATALOG.json (the catalog display data) is a separate release asset: dist/<id>_<version>_CATALOG.json.
# Owner, timestamps and compression are fixed so the output is reproducible.
# Usage: package.sh <version>
# Environment: PLUGIN_ID.
set -euo pipefail
version="${1:?usage: package.sh <version>}"
id="${PLUGIN_ID:?PLUGIN_ID is not set}"
ldflags="-s -w"
rm -rf dist && mkdir -p dist
for arch in amd64 arm64; do
  stage="$(mktemp -d)"
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -buildvcs=false -ldflags "$ldflags" -o "$stage/$id" .
  cp LICENSE NOTICE README.md "$stage/"
  name="${id}_${version}_linux_${arch}.tar.gz"
  tar -C "$stage" --sort=name --owner=0 --group=0 --numeric-owner --mtime='@0' --mode='u=rwX,go=rX' -cf - "$id" LICENSE NOTICE README.md | gzip -n -9 > "dist/$name"
  ( cd dist && sha256sum "$name" > "$name.sha256" )
  rm -rf "$stage"
done
cp CATALOG.json "dist/${id}_${version}_CATALOG.json"
( cd dist && sha256sum "${id}_${version}_CATALOG.json" > "${id}_${version}_CATALOG.json.sha256" )
ls -l dist
