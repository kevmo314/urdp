#define WIN32_LEAN_AND_MEAN
#include "native_windows.h"
#include <cfgmgr32.h>
#include <newdev.h>
#include <setupapi.h>
#include <shellapi.h>
#include <stdlib.h>
#include <wchar.h>

DWORD driver_find(const wchar_t *hardware, wchar_t *instance, DWORD capacity,
                  int *healthy) {
  HDEVINFO set = SetupDiGetClassDevsW(NULL, NULL, NULL, DIGCF_ALLCLASSES);
  if (set == INVALID_HANDLE_VALUE)
    return GetLastError();
  DWORD result = ERROR_NOT_FOUND;
  SP_DEVINFO_DATA dev = {.cbSize = sizeof(dev)};
  for (DWORD i = 0; SetupDiEnumDeviceInfo(set, i, &dev); ++i) {
    DWORD bytes = 0, type = 0;
    SetupDiGetDeviceRegistryPropertyW(set, &dev, SPDRP_HARDWAREID, &type, NULL,
                                      0, &bytes);
    if (!bytes || type != REG_MULTI_SZ)
      continue;
    // Extra terminators bound iteration even for malformed registry data.
    wchar_t *ids = calloc(1, bytes + 2 * sizeof(wchar_t));
    if (!ids) {
      result = ERROR_OUTOFMEMORY;
      break;
    }
    if (SetupDiGetDeviceRegistryPropertyW(set, &dev, SPDRP_HARDWAREID, &type,
                                          (BYTE *)ids, bytes, NULL)) {
      for (wchar_t *id = ids; *id; id += wcslen(id) + 1) {
        if (_wcsicmp(id, hardware))
          continue;
        if (SetupDiGetDeviceInstanceIdW(set, &dev, instance, capacity, NULL)) {
          ULONG status = 0, problem = 0;
          *healthy = CM_Get_DevNode_Status(&status, &problem, dev.DevInst, 0) ==
                         CR_SUCCESS &&
                     problem == 0 && (status & DN_STARTED);
          result = ERROR_SUCCESS;
        } else
          result = GetLastError();
        break;
      }
    }
    free(ids);
    if (result != ERROR_NOT_FOUND)
      break;
  }
  SetupDiDestroyDeviceInfoList(set);
  return result;
}

DWORD driver_create(const wchar_t *inf, const wchar_t *hardware,
                    wchar_t *instance, DWORD capacity, int *reboot) {
  GUID cls;
  wchar_t clsname[MAX_CLASS_NAME_LEN];
  if (!SetupDiGetINFClassW(inf, &cls, clsname, MAX_CLASS_NAME_LEN, NULL))
    return GetLastError();
  HDEVINFO set = SetupDiCreateDeviceInfoList(&cls, NULL);
  if (set == INVALID_HANDLE_VALUE)
    return GetLastError();
  SP_DEVINFO_DATA dev = {.cbSize = sizeof(dev)};
  DWORD err = ERROR_SUCCESS;
  BOOL registered = FALSE;
  size_t chars = wcslen(hardware) + 2;
  wchar_t *ids = calloc(chars, sizeof(wchar_t));
  if (!ids) {
    SetupDiDestroyDeviceInfoList(set);
    return ERROR_OUTOFMEMORY;
  }
  wcscpy(ids, hardware);
  if (!SetupDiCreateDeviceInfoW(set, clsname, &cls, NULL, NULL,
                                DICD_GENERATE_ID, &dev) ||
      !SetupDiSetDeviceRegistryPropertyW(set, &dev, SPDRP_HARDWAREID,
                                         (BYTE *)ids,
                                         (DWORD)(chars * sizeof(wchar_t))) ||
      !SetupDiCallClassInstaller(DIF_REGISTERDEVICE, set, &dev)) {
    err = GetLastError();
  } else {
    registered = TRUE;
    if (!SetupDiGetDeviceInstanceIdW(set, &dev, instance, capacity, NULL))
      err = GetLastError();
    BOOL restart = FALSE;
    // Do not force a downgrade over a newer installed driver.
    if (!err &&
        !UpdateDriverForPlugAndPlayDevicesW(NULL, hardware, inf, 0, &restart))
      err = GetLastError();
    *reboot = restart;
  }
  if (err && registered)
    SetupDiCallClassInstaller(DIF_REMOVE, set, &dev);
  free(ids);
  SetupDiDestroyDeviceInfoList(set);
  return err;
}

DWORD driver_remove(const wchar_t *instance, int *reboot) {
  HDEVINFO set = SetupDiCreateDeviceInfoList(NULL, NULL);
  if (set == INVALID_HANDLE_VALUE)
    return GetLastError();
  SP_DEVINFO_DATA dev = {.cbSize = sizeof(dev)};
  DWORD err = ERROR_SUCCESS;
  if (!SetupDiOpenDeviceInfoW(set, instance, NULL, 0, &dev)) {
    err = GetLastError();
    if (err == ERROR_NO_SUCH_DEVINST)
      err = ERROR_SUCCESS;
  } else if (!SetupDiCallClassInstaller(DIF_REMOVE, set, &dev))
    err = GetLastError();
  else {
    SP_DEVINSTALL_PARAMS_W params = {.cbSize = sizeof(params)};
    if (SetupDiGetDeviceInstallParamsW(set, &dev, &params))
      *reboot = !!(params.Flags & (DI_NEEDREBOOT | DI_NEEDRESTART));
  }
  SetupDiDestroyDeviceInfoList(set);
  return err;
}

DWORD driver_stage(const wchar_t *inf, wchar_t *published, DWORD capacity,
                   int *added) {
  if (SetupCopyOEMInfW(inf, NULL, SPOST_PATH, SP_COPY_NOOVERWRITE, published,
                       capacity, NULL, NULL)) {
    *added = 1;
    return ERROR_SUCCESS;
  }
  DWORD err = GetLastError();
  return err == ERROR_FILE_EXISTS ? ERROR_SUCCESS : err;
}

DWORD driver_elevate(const wchar_t *exe, const wchar_t *args,
                     DWORD *exit_code) {
  SHELLEXECUTEINFOW info = {0};
  info.cbSize = sizeof(info);
  info.fMask = SEE_MASK_NOCLOSEPROCESS | SEE_MASK_NOASYNC;
  info.lpVerb = L"runas";
  info.lpFile = exe;
  info.lpParameters = args;
  info.nShow = SW_HIDE;
  if (!ShellExecuteExW(&info))
    return GetLastError();
  DWORD wait = WaitForSingleObject(info.hProcess, 5 * 60 * 1000);
  DWORD err = ERROR_SUCCESS;
  if (wait != WAIT_OBJECT_0)
    err = wait == WAIT_TIMEOUT ? ERROR_TIMEOUT : GetLastError();
  else if (!GetExitCodeProcess(info.hProcess, exit_code))
    err = GetLastError();
  // Never terminate an installer midway through a driver operation.
  CloseHandle(info.hProcess);
  return err;
}
