#!/usr/bin/env bash
set -euo pipefail
PREFIX=${PREFIX:-"$(pwd)/third_party/windows"}
PREFIX=$(realpath -m "$PREFIX")
BUILD_DIR=${BUILD_DIR:-"$(pwd)/third_party/src"}
mkdir -p "$PREFIX" "$BUILD_DIR"
BUILD_DIR=$(realpath "$BUILD_DIR")
JOBS=${JOBS:-$(nproc)}
if [[ ! -f "$PREFIX/lib/libx264.a" ]]; then
    if [[ ! -d "$BUILD_DIR/x264/.git" ]]; then
        git clone --depth 1 --branch stable https://code.videolan.org/videolan/x264.git "$BUILD_DIR/x264"
    fi
    (
        cd "$BUILD_DIR/x264"
        ./configure --host=x86_64-w64-mingw32 --cross-prefix=x86_64-w64-mingw32- --enable-static --disable-cli --enable-pic --prefix="$PREFIX"
        make -j"$JOBS"
        make install
    )
fi
if [[ ! -f "$PREFIX/lib/libopus.a" ]]; then
    if [[ ! -f "$BUILD_DIR/opus-1.5.2.tar.gz" ]]; then
        curl -fL --retry 3 https://downloads.xiph.org/releases/opus/opus-1.5.2.tar.gz -o "$BUILD_DIR/opus-1.5.2.tar.gz.tmp"
        mv "$BUILD_DIR/opus-1.5.2.tar.gz.tmp" "$BUILD_DIR/opus-1.5.2.tar.gz"
    fi
    if [[ ! -d "$BUILD_DIR/opus-1.5.2" ]]; then
        tar -xzf "$BUILD_DIR/opus-1.5.2.tar.gz" -C "$BUILD_DIR"
    fi
    (
        cd "$BUILD_DIR/opus-1.5.2"
        ./configure --host=x86_64-w64-mingw32 --disable-shared --disable-doc --disable-extra-programs --prefix="$PREFIX"
        make -j"$JOBS"
        make install
    )
fi
