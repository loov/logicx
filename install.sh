#!/bin/zsh
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Installs the Export to MusicXML app, a Quick Action that macOS shows in the
# Services menu of every app (Logic Pro included), and the command line tool.
# The app and the tool are the same binary: with arguments it is a command,
# without them it is the app.
set -eu

bin=${BIN_DIR:-$HOME/bin}
app="${APP_DIR:-$HOME/Applications}/Export to MusicXML.app"
service="$HOME/Library/Services/Export to MusicXML.workflow"

mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources" "$bin"
go build -o "$app/Contents/MacOS/logicx-to-musicxml" ./cmd/logicx-to-musicxml
ln -sf "$app/Contents/MacOS/logicx-to-musicxml" "$bin/logicx-to-musicxml"
cp cmd/logicx-to-musicxml/icon.icns "$app/Contents/Resources/icon.icns"
printf 'APPL????' > "$app/Contents/PkgInfo"

cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleExecutable</key>
	<string>logicx-to-musicxml</string>
	<key>CFBundleIconFile</key>
	<string>icon</string>
	<key>CFBundleIdentifier</key>
	<string>net.egonelbre.logicx.export</string>
	<key>CFBundleName</key>
	<string>Export to MusicXML</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>1.0</string>
	<key>CFBundleDocumentTypes</key>
	<array>
		<dict>
			<key>CFBundleTypeExtensions</key>
			<array>
				<string>logicx</string>
			</array>
			<key>CFBundleTypeName</key>
			<string>Logic Pro Project</string>
			<key>CFBundleTypeRole</key>
			<string>Viewer</string>
			<key>LSTypeIsPackage</key>
			<true/>
		</dict>
	</array>
	<key>LSMinimumSystemVersion</key>
	<string>10.13</string>
	<key>NSHighResolutionCapable</key>
	<true/>
</dict>
</plist>
PLIST

/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$app"

mkdir -p "$service/Contents"
cat > "$service/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>NSServices</key>
	<array>
		<dict>
			<key>NSMenuItem</key>
			<dict>
				<key>default</key>
				<string>Export to MusicXML</string>
			</dict>
			<key>NSMessage</key>
			<string>runWorkflowAsService</string>
			<key>NSSendFileTypes</key>
			<array/>
			<key>NSSendTypes</key>
			<array/>
		</dict>
	</array>
</dict>
</plist>
PLIST

cat > "$service/Contents/document.wflow" <<WFLOW
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>AMApplicationBuild</key>
	<string>521</string>
	<key>AMApplicationVersion</key>
	<string>2.10</string>
	<key>AMDocumentVersion</key>
	<string>2</string>
	<key>actions</key>
	<array>
		<dict>
			<key>action</key>
			<dict>
				<key>AMAccepts</key>
				<dict>
					<key>Container</key>
					<string>List</string>
					<key>Optional</key>
					<true/>
					<key>Types</key>
					<array>
						<string>com.apple.cocoa.string</string>
					</array>
				</dict>
				<key>AMActionVersion</key>
				<string>2.0.3</string>
				<key>AMProvides</key>
				<dict>
					<key>Container</key>
					<string>List</string>
					<key>Types</key>
					<array>
						<string>com.apple.cocoa.string</string>
					</array>
				</dict>
				<key>ActionBundlePath</key>
				<string>/System/Library/Automator/Run Shell Script.action</string>
				<key>ActionName</key>
				<string>Run Shell Script</string>
				<key>ActionParameters</key>
				<dict>
					<key>COMMAND_STRING</key>
					<string>exec /usr/bin/open -a "$app"</string>
					<key>CheckedForUserDefaultShell</key>
					<true/>
					<key>inputMethod</key>
					<integer>0</integer>
					<key>shell</key>
					<string>/bin/zsh</string>
					<key>source</key>
					<string></string>
				</dict>
				<key>BundleIdentifier</key>
				<string>com.apple.RunShellScript</string>
				<key>CFBundleVersion</key>
				<string>2.0.3</string>
				<key>CanShowSelectedItemsWhenRun</key>
				<false/>
				<key>CanShowWhenRun</key>
				<true/>
				<key>Category</key>
				<array>
					<string>AMCategoryUtilities</string>
				</array>
				<key>Class Name</key>
				<string>RunShellScriptAction</string>
				<key>InputUUID</key>
				<string>C8AF69B2-6B71-4C2E-9F1B-1D6F6D1A0001</string>
				<key>Keywords</key>
				<array>
					<string>Shell</string>
					<string>Script</string>
					<string>Command</string>
					<string>Run</string>
					<string>Unix</string>
				</array>
				<key>OutputUUID</key>
				<string>C8AF69B2-6B71-4C2E-9F1B-1D6F6D1A0002</string>
				<key>UUID</key>
				<string>C8AF69B2-6B71-4C2E-9F1B-1D6F6D1A0003</string>
				<key>UnlocalizedApplications</key>
				<array>
					<string>Automator</string>
				</array>
				<key>arguments</key>
				<dict/>
				<key>isViewVisible</key>
				<integer>1</integer>
			</dict>
			<key>isViewVisible</key>
			<integer>1</integer>
		</dict>
	</array>
	<key>workflowMetaData</key>
	<dict>
		<key>serviceInputTypeIdentifier</key>
		<string>com.apple.Automator.nothing</string>
		<key>serviceOutputTypeIdentifier</key>
		<string>com.apple.Automator.nothing</string>
		<key>serviceProcessesInput</key>
		<integer>0</integer>
		<key>workflowTypeIdentifier</key>
		<string>com.apple.Automator.servicesMenu</string>
	</dict>
</dict>
</plist>
WFLOW

/System/Library/CoreServices/pbs -flush

echo "Installed $app"
echo "Command line: $bin/logicx-to-musicxml"
echo "Quick Action: Logic Pro > Services > Export to MusicXML"
echo "Assign a shortcut in System Settings > Keyboard > Keyboard Shortcuts > Services."
