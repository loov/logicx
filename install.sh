#!/bin/zsh
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Builds and installs the app for this machine, without signing. The app is
# also the command line tool, so it is linked into $HOME/bin as well.
# For a signed, notarized disk image to hand to other people, see release.sh.
set -eu

cd "$(dirname "$0")"

app="${APP_DIR:-$HOME/Applications}/MusicXML Bridge for Logic Pro.app"
bin=${BIN_DIR:-$HOME/bin}

# The app answered to another name before, and two copies would mean two
# entries in the Services menu.
rm -rf "${APP_DIR:-$HOME/Applications}/Export to MusicXML.app"

./build-app.sh "$app"
mkdir -p "$bin"
ln -sf "$app/Contents/MacOS/logicx-to-musicxml" "$bin/logicx-to-musicxml"

# Teaches LaunchServices about the app, its dropped project types, and the
# Services menu item it provides.
/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$app"
/System/Library/CoreServices/pbs -flush

echo "Installed $app"
echo "Command line: $bin/logicx-to-musicxml"
echo "Menu item: Logic Pro > Services > Export to MusicXML"
echo "Assign a shortcut in System Settings > Keyboard > Keyboard Shortcuts > Services."
