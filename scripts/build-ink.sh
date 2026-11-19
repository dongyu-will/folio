#!/bin/sh
set -eu
if [ "$(uname -s)" != Darwin ]; then
  exit 0
fi
mkdir -p resources/darwin
for renderer in ink pdf; do
  output="resources/darwin/mgnote-$renderer"
  xcrun swiftc -O -target arm64-apple-macos12.0 "scripts/render-$renderer.swift" -o "$output.arm64"
  xcrun swiftc -O -target x86_64-apple-macos12.0 "scripts/render-$renderer.swift" -o "$output.x86_64"
  xcrun lipo -create "$output.arm64" "$output.x86_64" -output "$output"
  rm "$output.arm64" "$output.x86_64"
done
