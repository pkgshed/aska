#!/bin/sh
# Install the aska command into DESTDIR/PREFIX/bin.
#
# Usage: scripts/install.sh PREFIX [DESTDIR]
#
# PREFIX is where the command lives at run time and must be absolute.
# DESTDIR is prepended verbatim, following the GNU convention for staged
# installs, so that distribution packaging can install into a build root
# such as $pkgdir without changing PREFIX. The command must have been built
# into bin/ beforehand, for example with "just build".
set -eu

usage="usage: scripts/install.sh PREFIX [DESTDIR]"
prefix=${1:?$usage}
destdir=${2:-}

case $prefix in
/*) ;;
*)
    echo "install: PREFIX must be an absolute path, got '$prefix'" >&2
    exit 2
    ;;
esac

project_dir=$(cd "$(dirname "$0")/.." && pwd)
binary="$project_dir/bin/aska"
bindir="$destdir$prefix/bin"

if [ ! -x "$binary" ]; then
    echo "install: missing $binary; build it first with just build" >&2
    exit 1
fi

install -d "$bindir"
install -m 0755 "$binary" "$bindir/aska"
echo "installed $bindir/aska"
