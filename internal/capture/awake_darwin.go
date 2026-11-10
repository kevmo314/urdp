package capture

// A sleeping display has nothing to capture: ScreenCaptureKit then reports
// no displays at all. Viewers wake it, and it's kept awake while any source
// is open (the system's idle display sleep would otherwise blank it under
// a remote user, who isn't generating local activity).

/*
#cgo darwin LDFLAGS: -framework IOKit -framework CoreFoundation
#include <IOKit/pwr_mgt/IOPMLib.h>

static IOPMAssertionID activity;
static void ur_wake(void) {
	IOPMAssertionDeclareUserActivity(CFSTR("urdp viewer"), kIOPMUserActiveLocal, &activity);
}
static uint32_t ur_keep_awake(void) {
	IOPMAssertionID id = 0;
	IOPMAssertionCreateWithName(kIOPMAssertPreventUserIdleDisplaySleep, kIOPMAssertionLevelOn,
		CFSTR("urdp is streaming the screen"), &id);
	return id;
}
static void ur_allow_sleep(uint32_t id) { if (id) IOPMAssertionRelease(id); }
*/
import "C"

import "sync"

var awake struct {
	sync.Mutex
	holders int
	id      C.uint32_t
}

// wakeDisplay turns the display on if it's asleep.
func wakeDisplay() { C.ur_wake() }

// keepAwake wakes the display and keeps it on until the returned function
// is called (once per keepAwake).
func keepAwake() func() {
	awake.Lock()
	defer awake.Unlock()
	C.ur_wake()
	if awake.holders == 0 {
		awake.id = C.ur_keep_awake()
	}
	awake.holders++
	var once sync.Once
	return func() {
		once.Do(func() {
			awake.Lock()
			defer awake.Unlock()
			if awake.holders--; awake.holders == 0 {
				C.ur_allow_sleep(awake.id)
				awake.id = 0
			}
		})
	}
}
