#pragma once
#include <stdint.h>
#include <stddef.h>
#include <windows.h>

#define URDP_CAMERA_CLSID L"{D8B04D25-43B5-4B57-9B1B-DA8E5F2D6C90}"
#define URDP_CAMERA_KEY L"SOFTWARE\\Classes\\CLSID\\" URDP_CAMERA_CLSID
#define URDP_CAMERA_MAPPING L"Global\\urdp.Camera.v1"
#define URDP_CAMERA_WIDTH 4096
#define URDP_CAMERA_HEIGHT 2160
#define URDP_CAMERA_BYTES (URDP_CAMERA_WIDTH * URDP_CAMERA_HEIGHT * 3 / 2)
#define URDP_CAMERA_MAGIC 0x55434131

// Fixed size slots allow input resolution changes without remapping. Sequence
// numbers bracket writes so a reader never publishes a partially copied frame.
struct CameraSlot {
    volatile LONG sequence;
    uint32_t width, height;
    int64_t timestamp;
    uint8_t pixels[URDP_CAMERA_BYTES];
};
struct CameraRing {
    uint32_t magic, version;
    volatile LONG counter;
    CameraSlot slots[3];
};

static_assert(offsetof(CameraSlot, pixels) == 24, "camera slot ABI");
static_assert(offsetof(CameraRing, slots) == 16, "camera ring ABI");
