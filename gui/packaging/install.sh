#!/bin/sh
# Installs the hub GUI: binary, .desktop entry and icons.
# Usage: sudo sh gui/packaging/install.sh [path/to/xs-hub-gui]   (PREFIX, default /usr/local)
# The GUI talks to xs-hubd over its socket: the desktop user must be in the
# group crossstream (sudo usermod -aG crossstream $USER, then log in again).
set -eu
here=$(cd "$(dirname "$0")" && pwd)
bin=${1:-$here/../build/bin/xs-hub-gui}
prefix=${PREFIX:-/usr/local}
[ -f "$bin" ] || { echo "no GUI binary at $bin (run make gui first)" >&2; exit 1; }
install -Dm755 "$bin" "$prefix/bin/xs-hub-gui"
install -Dm644 "$here/crossstream-hub.desktop" "$prefix/share/applications/crossstream-hub.desktop"
# Absolute Exec: ~/.local/bin (PREFIX=$HOME/.local) is often not on the session PATH.
sed -i "s#^Exec=.*#Exec=$prefix/bin/xs-hub-gui#" "$prefix/share/applications/crossstream-hub.desktop"
install -Dm644 "$here/crossstream-hub.svg" "$prefix/share/icons/hicolor/scalable/apps/crossstream-hub.svg"
install -Dm644 "$here/crossstream-hub.png" "$prefix/share/icons/hicolor/256x256/apps/crossstream-hub.png"
gtk-update-icon-cache -q -t "$prefix/share/icons/hicolor" 2>/dev/null || true
update-desktop-database -q "$prefix/share/applications" 2>/dev/null || true
echo "installed xs-hub-gui to $prefix/bin (menu entry: CrossStream Hub)"
