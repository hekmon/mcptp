#!/usr/bin/env bash

set -e

version=$(git describe --tags --always --dirty)

crosscompile () {
    if [ "$1" == "windows" ]; then
        name='mcptp.exe'
    else
        name='mcptp'
    fi
    GOOS="${1}" GOARCH="${2}" go build -ldflags='-s' -o "$name"
    zip -9 "mcptp_${version}_${1}_${2}.zip" "$name"
}

echo '* Compiling for Windows'
crosscompile 'windows' 'amd64'
echo
echo '* Compiling for MacOS Intel'
crosscompile 'darwin' 'amd64'
echo
echo '* Compiling for MacOS Apple Silicon'
crosscompile 'darwin' 'arm64'
echo
echo '* Compiling for Linux'
crosscompile 'linux' 'amd64'
echo
echo '* Cleaning up'
test -f mcptp && rm mcptp
test -f mcptp.exe && rm mcptp.exe
