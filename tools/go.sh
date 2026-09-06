#!/usr/bin/env bash
# 建置與測試一律在 docker 裡（spec 001 §5）。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="${PC98GOLEM_GO_IMAGE:-coab-go-ebiten:1.24}"
mkdir -p "$ROOT/workplace/go-build-cache" "$ROOT/workplace/go-mod-cache"
exec docker run --rm --network none --memory 3g --cpus 2 --pids-limit 512 \
  --log-opt max-size=10m --log-opt max-file=3 \
  -u "$(id -u):$(id -g)" \
  -v "$ROOT:/src" \
  -v "$ROOT/workplace/go-build-cache:/gocache" \
  -v "$ROOT/workplace/go-mod-cache:/gomod" \
  ${PC98_DISK_DIR:+-v "$PC98_DISK_DIR:/disks:ro"} \
  ${PC98_DISK_DIR:+-e PC98_DISK_DIR=/disks} \
  -e GOCACHE=/gocache -e GOMODCACHE=/gomod -w /src \
  "$IMAGE" /usr/local/go/bin/go "$@"
