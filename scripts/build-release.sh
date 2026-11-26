#!/usr/bin/env bash
set -euo pipefail

version="${1:?Usage: bash scripts/build-release.sh TAG OUTPUT_DIRECTORY}"
output="${2:?Output directory required}"
if [[ ! "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.(0|[1-9][0-9]*))?$ ]]; then
    echo "Invalid release tag: $version" >&2
    exit 1
fi

revision="$(git rev-parse HEAD)"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
mkdir -p "$output"
for os in linux darwin; do
    for arch in amd64 arm64; do
        CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -buildvcs=false \
            -ldflags="-s -w -X main.version=$version -X main.revision=$revision" \
            -o "$stage/pushboy" ./cmd/pushboy
        git -c tar.umask=0022 archive --format=tar --add-file="$stage/pushboy" \
            "$revision" LICENSE db/migrations/postgres | gzip -n > "$output/pushboy_${version}_${os}_${arch}.tar.gz"
    done
done

(
    cd "$output"
    LC_ALL=C sha256sum "pushboy_${version}_"*.tar.gz > SHA256SUMS
)
