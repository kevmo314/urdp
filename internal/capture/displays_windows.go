package capture

/*
#include "native_windows.h"
*/
import "C"

import "unsafe"

func windowsDisplays() ([]C.capture_display, error) {
	dpiOnce.Do(func() { C.capture_dpi() })
	var items *C.capture_display
	var count C.int
	if hr := C.capture_displays(&items, &count); hr < 0 {
		return nil, captureError("list displays", hr)
	}
	defer C.capture_free(unsafe.Pointer(items))
	return append([]C.capture_display(nil), unsafe.Slice(items, int(count))...), nil
}

func listDisplays() ([]Display, error) {
	items, err := windowsDisplays()
	if err != nil {
		return nil, err
	}
	displays := make([]Display, 0, len(items))
	for i, item := range items {
		r := item.rect
		displays = append(displays, Display{
			Index: i + 1, Name: C.GoString(&item.name[0]),
			X: int(r.x), Y: int(r.y), Width: int(r.w), Height: int(r.h),
			Primary: item.primary != 0,
		})
	}
	return displays, nil
}

func openDisplay(i int) (Source, error) {
	items, err := windowsDisplays()
	if err != nil {
		return nil, err
	}
	if i < 1 || i > len(items) {
		return nil, ErrNotFound
	}
	s := &windowsSource{}
	item := items[i-1]
	if hr := C.capture_open_display(item.adapter, item.output, &s.native); hr < 0 {
		return nil, captureError("open display", hr)
	}
	if err := s.resize(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
