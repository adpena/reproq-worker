#!/bin/bash
set -e

# Build directory
mkdir -p dist

platforms=("darwin/amd64" "darwin/arm64" "linux/amd64" "linux/arm64" "windows/amd64")

for platform in "${platforms[@]}"
do
    platform_split=(${platform//\// })
    GOOS=${platform_split[0]}
    GOARCH=${platform_split[1]}
    output_name="reproq-$GOOS-$GOARCH"
    if [ $GOOS = "windows" ]; then
        output_name+='.exe'
    fi

    echo "🏗️ Building for $GOOS/$GOARCH..."
    export GO111MODULE=on
    export GOOS=$GOOS
    export GOARCH=$GOARCH
    go build -tags prod -o "dist/$output_name" ./cmd/reproq
done

echo "✅ All binaries built in dist/"
ls -lh dist/
