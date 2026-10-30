// Exercises the driver through its plug-in interface, the way coreaudiod
// calls it, without installing it. Run with `make mac-driver-test`.

#include "urdp_audio.c"

#include <math.h>
#include <stdio.h>
#include <stdlib.h>

static int failures;

#define CHECK(cond)                                                          \
	do {                                                                     \
		if (!(cond)) {                                                       \
			fprintf(stderr, "%s:%d: check failed: %s\n", __FILE__, __LINE__, #cond); \
			failures++;                                                      \
		}                                                                    \
	} while (0)

static AudioServerPlugInDriverRef drv;

static OSStatus get(AudioObjectID obj, AudioObjectPropertySelector sel, AudioObjectPropertyScope scope,
		    UInt32 qsize, const void *q, UInt32 size, void *out, UInt32 *outSize) {
	AudioObjectPropertyAddress a = {sel, scope, kAudioObjectPropertyElementMain};
	return (*drv)->GetPropertyData(drv, obj, 0, &a, qsize, q, size, outSize, out);
}

static void io(UInt32 op, AudioObjectID stream, Float64 t, float *buf, UInt32 frames) {
	AudioServerPlugInIOCycleInfo c = {0};
	c.mInputTime.mSampleTime = t;
	c.mOutputTime.mSampleTime = t;
	CHECK((*drv)->DoIOOperation(drv, kObjDevice, stream, 1, op, frames, &c, buf, NULL) == 0);
}

int main(void) {
	drv = urdp_audio_create(NULL, kAudioServerPlugInTypeUUID);
	CHECK(drv != NULL);
	CHECK(urdp_audio_create(NULL, kAudioServerPlugInDriverInterfaceUUID) == NULL);
	void *iface = NULL;
	CHECK((*drv)->QueryInterface(drv, CFUUIDGetUUIDBytes(kAudioServerPlugInDriverInterfaceUUID), &iface) == S_OK);
	CHECK(iface == drv);
	(*drv)->Release(drv);
	static AudioServerPlugInHostInterface host;
	CHECK((*drv)->Initialize(drv, &host) == 0);

	UInt32 n;
	AudioObjectID ids[4];
	CHECK(get(kObjPlugIn, kAudioPlugInPropertyDeviceList, kAudioObjectPropertyScopeGlobal, 0, NULL, sizeof ids, ids, &n) == 0);
	CHECK(n == sizeof(AudioObjectID) && ids[0] == kObjDevice);

	CFStringRef uid = CFSTR(DEVICE_UID), other = CFSTR("nope");
	AudioObjectID dev;
	CHECK(get(kObjPlugIn, kAudioPlugInPropertyTranslateUIDToDevice, kAudioObjectPropertyScopeGlobal,
		  sizeof uid, &uid, sizeof dev, &dev, &n) == 0 && dev == kObjDevice);
	CHECK(get(kObjPlugIn, kAudioPlugInPropertyTranslateUIDToDevice, kAudioObjectPropertyScopeGlobal,
		  sizeof other, &other, sizeof dev, &dev, &n) == 0 && dev == kAudioObjectUnknown);

	CFStringRef name = NULL;
	CHECK(get(kObjDevice, kAudioObjectPropertyName, kAudioObjectPropertyScopeGlobal, 0, NULL, sizeof name, &name, &n) == 0);
	CHECK(name && CFStringCompare(name, CFSTR("urdp Microphone"), 0) == kCFCompareEqualTo);

	CHECK(get(kObjDevice, kAudioDevicePropertyStreams, kAudioObjectPropertyScopeInput, 0, NULL, sizeof ids, ids, &n) == 0);
	CHECK(n == sizeof(AudioObjectID) && ids[0] == kObjInputStream);
	CHECK(get(kObjDevice, kAudioDevicePropertyStreams, kAudioObjectPropertyScopeOutput, 0, NULL, sizeof ids, ids, &n) == 0);
	CHECK(n == sizeof(AudioObjectID) && ids[0] == kObjOutputStream);
	CHECK(get(kObjDevice, kAudioObjectPropertyOwnedObjects, kAudioObjectPropertyScopeGlobal, 0, NULL, sizeof ids, ids, &n) == 0);
	CHECK(n == 2 * sizeof(AudioObjectID));
	// Arrays are truncated to what fits, scalars are refused.
	CHECK(get(kObjDevice, kAudioObjectPropertyOwnedObjects, kAudioObjectPropertyScopeGlobal, 0, NULL, 6, ids, &n) == 0);
	CHECK(n == sizeof(AudioObjectID));
	Float64 rate;
	CHECK(get(kObjDevice, kAudioDevicePropertyNominalSampleRate, kAudioObjectPropertyScopeGlobal, 0, NULL, 4, &rate, &n) ==
	      kAudioHardwareBadPropertySizeError);
	CHECK(get(kObjDevice, kAudioDevicePropertyNominalSampleRate, kAudioObjectPropertyScopeGlobal, 0, NULL, sizeof rate, &rate, &n) == 0 &&
	      rate == 48000);

	UInt32 def;
	CHECK(get(kObjDevice, kAudioDevicePropertyDeviceCanBeDefaultDevice, kAudioObjectPropertyScopeInput, 0, NULL, sizeof def, &def, &n) == 0 && def);
	CHECK(get(kObjDevice, kAudioDevicePropertyDeviceCanBeDefaultDevice, kAudioObjectPropertyScopeOutput, 0, NULL, sizeof def, &def, &n) == 0 && !def);

	AudioStreamBasicDescription f;
	CHECK(get(kObjInputStream, kAudioStreamPropertyVirtualFormat, kAudioObjectPropertyScopeGlobal, 0, NULL, sizeof f, &f, &n) == 0);
	CHECK(f.mSampleRate == 48000 && f.mChannelsPerFrame == 2 && f.mBitsPerChannel == 32 && (f.mFormatFlags & kAudioFormatFlagIsFloat));
	UInt32 dir;
	CHECK(get(kObjInputStream, kAudioStreamPropertyDirection, kAudioObjectPropertyScopeGlobal, 0, NULL, sizeof dir, &dir, &n) == 0 && dir == 1);

	AudioObjectPropertyAddress bogus = {'zzzz', kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
	CHECK(!(*drv)->HasProperty(drv, kObjDevice, 0, &bogus));
	AudioObjectPropertyAddress tr = {kAudioPlugInPropertyTranslateUIDToDevice, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
	CHECK((*drv)->HasProperty(drv, kObjPlugIn, 0, &tr));

	// Loopback: what's written to the output at sample time t is read
	// from the input at t.
	CHECK((*drv)->StartIO(drv, kObjDevice, 1) == 0);
	Float64 st;
	UInt64 ht, seed;
	CHECK((*drv)->GetZeroTimeStamp(drv, kObjDevice, 1, &st, &ht, &seed) == 0 && st == 0 && seed == 1);
	Boolean will, inPlace;
	(*drv)->WillDoIOOperation(drv, kObjDevice, 1, kAudioServerPlugInIOOperationWriteMix, &will, &inPlace);
	CHECK(will);
	(*drv)->WillDoIOOperation(drv, kObjDevice, 1, kAudioServerPlugInIOOperationProcessOutput, &will, &inPlace);
	CHECK(!will);

	enum { F = 512 };
	float out[F * 2], in[F * 2];
	Float64 t = 3 * RING_FRAMES - 100; // writes wrap around the ring
	for (int i = 0; i < F * 2; i++)
		out[i] = sinf(i * 0.01f);
	io(kAudioServerPlugInIOOperationWriteMix, kObjOutputStream, t, out, F);
	memset(in, 0xff, sizeof in);
	io(kAudioServerPlugInIOOperationReadInput, kObjInputStream, t, in, F);
	CHECK(memcmp(in, out, sizeof in) == 0);

	// Reading past the last write gives silence, not stale ring contents.
	io(kAudioServerPlugInIOOperationReadInput, kObjInputStream, t + F + RING_FRAMES, in, F);
	for (int i = 0; i < F * 2; i++)
		CHECK(in[i] == 0);
	// A later write clears the gap after the previous one.
	io(kAudioServerPlugInIOOperationWriteMix, kObjOutputStream, t + F + 1000, out, F);
	io(kAudioServerPlugInIOOperationReadInput, kObjInputStream, t + F, in, F);
	for (int i = 0; i < F * 2; i++)
		CHECK(in[i] == 0);
	// Half old (overlapping the new write's start) reads back correctly.
	io(kAudioServerPlugInIOOperationReadInput, kObjInputStream, t + F + 1000 - F / 2, in, F);
	CHECK(in[0] == 0 && memcmp(in + F, out, F * sizeof(float)) == 0);

	UInt32 running;
	CHECK(get(kObjDevice, kAudioDevicePropertyDeviceIsRunning, kAudioObjectPropertyScopeGlobal, 0, NULL, sizeof running, &running, &n) == 0 && running);
	CHECK((*drv)->StopIO(drv, kObjDevice, 1) == 0);
	CHECK(get(kObjDevice, kAudioDevicePropertyDeviceIsRunning, kAudioObjectPropertyScopeGlobal, 0, NULL, sizeof running, &running, &n) == 0 && !running);

	if (failures) {
		fprintf(stderr, "FAIL: %d checks\n", failures);
		return 1;
	}
	printf("ok\n");
	return 0;
}
