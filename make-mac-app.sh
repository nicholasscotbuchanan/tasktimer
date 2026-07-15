#!/bin/bash

# Variables
APP_NAME="TaskTimer"         # Replace with your app name
MAIN_GO_FILE="TaskTimer.go"   # Replace with your main Go file

# Ensure Go is installed
if ! command -v go &> /dev/null; then
    echo "Go is not installed. Please install Go and try again."
    exit 1
fi

# Compile the Go program
echo "Compiling Go program..."
go build -o "$APP_NAME"
cd sync 
go build -o "${APP_NAME}-JsonSync"
cd ..

if [ $? -ne 0 ]; then
    echo "Failed to compile the Go program."
    exit 1
fi

# Create the .app bundle structure
echo "Creating .app bundle structure..."
APP_BUNDLE="$APP_NAME.app"
CONTENTS_DIR="$APP_BUNDLE/Contents"
MACOS_DIR="$CONTENTS_DIR/MacOS"
RESOURCES_DIR="$CONTENTS_DIR/Resources"

# Remove any existing .app bundle
rm -rf "$APP_BUNDLE"
rm -rf "out/$APP_BUNDLE"

# Create necessary directories
mkdir -p "$MACOS_DIR"
mkdir -p "$RESOURCES_DIR/var"
mkdir -p out

# Move the compiled binary into the MacOS directory
mv "$APP_NAME" "$MACOS_DIR/"
mv "sync/$APP_NAME-JsonSync" "$MACOS_DIR/"

echo "Linking"
ln -s ../Resources/var "$MACOS_DIR/var"

# Create Info.plist file
cat > "$CONTENTS_DIR/Info.plist" <<EOL
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key>
    <string>$APP_NAME</string>
    <key>CFBundleIdentifier</key>
    <string>com.example.$APP_NAME</string>
    <key>CFBundleName</key>
    <string>$APP_NAME</string>
    <key>CFBundleVersion</key>
    <string>1.0</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
</dict>
</plist>
EOL

mv "$APP_BUNDLE" out

echo "The application bundle $APP_NAME.app has been created successfully."
