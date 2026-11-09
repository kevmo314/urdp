#include <windows.h>
// Results are Win32 errors. Buffers contain UTF-16 strings.
DWORD driver_find(const wchar_t *hardware, wchar_t *instance, DWORD capacity,
                  int *healthy);
DWORD driver_create(const wchar_t *inf, const wchar_t *hardware,
                    wchar_t *instance, DWORD capacity, int *reboot);
DWORD driver_remove(const wchar_t *instance, int *reboot);
DWORD driver_stage(const wchar_t *inf, wchar_t *published, DWORD capacity,
                   int *added);
DWORD driver_elevate(const wchar_t *exe, const wchar_t *args, DWORD *exit_code);
