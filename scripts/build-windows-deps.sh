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
if [[ ! -f "$PREFIX/lib/libvpx.a" || ! -f "$PREFIX/lib/pkgconfig/vpx.pc" ]]; then
    if [[ ! -d "$BUILD_DIR/libvpx/.git" ]]; then
        git clone --depth 1 --branch v1.15.2 https://chromium.googlesource.com/webm/libvpx "$BUILD_DIR/libvpx"
    fi
    (
        cd "$BUILD_DIR/libvpx"
        if [[ -f vpx_config.h ]]; then make distclean; fi
        CROSS=x86_64-w64-mingw32- ./configure --target=x86_64-win64-gcc --prefix="$PREFIX" \
            --enable-static --disable-shared --disable-examples --disable-tools --disable-docs \
            --disable-unit-tests --disable-multithread --enable-vp8 --disable-vp8-encoder --disable-vp9-encoder \
            --disable-vp9-decoder
        make -j"$JOBS"
        make install
    )
fi

# A minimal FFmpeg with only the hardware encoders (AMF, NVENC, Media
# Foundation) and D3D11 frames. The vendor runtimes (amfrt64.dll,
# nvEncodeAPI64.dll, mfplat.dll) are all loaded at run time, so urdp.exe
# still starts on machines without them.
FFMPEG_VERSION=n8.1.3
NVHEADERS_VERSION=n13.0.19.1
AMF_VERSION=v1.5.3
if [[ ! -f "$PREFIX/lib/libavcodec.a" || ! -f "$PREFIX/.ffmpeg-$FFMPEG_VERSION" ]]; then
    if [[ ! -d "$BUILD_DIR/nv-codec-headers-$NVHEADERS_VERSION" ]]; then
        git clone --depth 1 --branch "$NVHEADERS_VERSION" https://github.com/FFmpeg/nv-codec-headers "$BUILD_DIR/nv-codec-headers-$NVHEADERS_VERSION"
    fi
    make -C "$BUILD_DIR/nv-codec-headers-$NVHEADERS_VERSION" PREFIX="$PREFIX" install >/dev/null
    if [[ ! -d "$BUILD_DIR/amf-$AMF_VERSION" ]]; then
        # Headers only (amf/public/include), MIT licensed.
        git clone --depth 1 --branch "$AMF_VERSION" --filter=blob:none --sparse \
            https://github.com/GPUOpen-LibrariesAndSDKs/AMF "$BUILD_DIR/amf-$AMF_VERSION"
        git -C "$BUILD_DIR/amf-$AMF_VERSION" sparse-checkout set amf/public/include
    fi
    mkdir -p "$PREFIX/include/AMF"
    cp -r "$BUILD_DIR/amf-$AMF_VERSION/amf/public/include/." "$PREFIX/include/AMF/"
    if [[ ! -d "$BUILD_DIR/ffmpeg-$FFMPEG_VERSION" ]]; then
        git clone --depth 1 --branch "$FFMPEG_VERSION" https://git.ffmpeg.org/ffmpeg.git "$BUILD_DIR/ffmpeg-$FFMPEG_VERSION"
    fi
    (
        cd "$BUILD_DIR/ffmpeg-$FFMPEG_VERSION"
        if [[ -f ffbuild/config.mak ]]; then make distclean >/dev/null; fi
        PKG_CONFIG_LIBDIR="$PREFIX/lib/pkgconfig" ./configure --target-os=mingw32 --arch=x86_64 \
            --cross-prefix=x86_64-w64-mingw32- --pkg-config=pkg-config --prefix="$PREFIX" \
            --enable-static --disable-shared --disable-everything --disable-autodetect \
            --disable-programs --disable-doc --disable-network --disable-avformat --disable-avfilter \
            --disable-avdevice --disable-swscale --disable-swresample \
            --enable-d3d11va --enable-amf --enable-ffnvcodec --enable-nvenc --enable-mediafoundation \
            --enable-encoder=h264_amf,hevc_amf,av1_amf,h264_nvenc,hevc_nvenc,av1_nvenc,h264_mf,hevc_mf \
            --extra-cflags="-I$PREFIX/include"
        make -j"$JOBS"
        make install
    )
    # cgo runs pkg-config without --static; fold the private libraries in.
    for pc in libavcodec libavutil; do
        f="$PREFIX/lib/pkgconfig/$pc.pc"
        priv=$(sed -n 's/^Libs.private: *//p' "$f")
        sed -i "s|^Libs: \(.*\)|Libs: \1 $priv|; /^Libs.private:/d" "$f"
    done
    touch "$PREFIX/.ffmpeg-$FFMPEG_VERSION"
fi

# The embedded Windows 11 source uses only system DLLs and a static C++ runtime.
camera_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../internal/vdev/mfcam" && pwd)"
if [[ ! -f "$camera_dir/urdpcam.dll" || "$camera_dir/source.cpp" -nt "$camera_dir/urdpcam.dll" || "$camera_dir/shared.h" -nt "$camera_dir/urdpcam.dll" || "$camera_dir/sdk.h" -nt "$camera_dir/urdpcam.dll" || "$camera_dir/attributes.h" -nt "$camera_dir/urdpcam.dll" || "$camera_dir/../camconv/convert.h" -nt "$camera_dir/urdpcam.dll" ]]; then
    x86_64-w64-mingw32-g++ -std=c++17 -O2 -Wall -Wextra -Werror -shared -static -static-libgcc -static-libstdc++ \
        -o "$camera_dir/urdpcam.dll.tmp" "$camera_dir/source.cpp" -lmfplat -lmfuuid -lole32 -ladvapi32 -lksuser -luuid
    mv "$camera_dir/urdpcam.dll.tmp" "$camera_dir/urdpcam.dll"
fi
