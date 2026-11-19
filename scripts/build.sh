#!/bin/sh
# Build the aska command into bin/, stamping the version from git.
#
# Usage: scripts/build.sh [output]
set -eu

project_dir=$(cd "$(dirname "$0")/.." && pwd)
output=${1:-"$project_dir/bin/aska"}
version=$(git -C "$project_dir" describe --tags --always --dirty 2>/dev/null || echo devel)

cd "$project_dir"
go build \
    -trimpath \
    -ldflags "-s -w -X github.com/pkgshed/aska/internal/cli.Version=$version" \
    -o "$output" \
    ./cmd/aska
echo "built $output ($version)"
