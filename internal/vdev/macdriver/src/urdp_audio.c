// urdp Microphone: a CoreAudio AudioServerPlugIn (HAL driver) with one
// loopback device. Whatever is played into its output stream comes back
// out of its input stream, so urdp plays the viewer's microphone into the
// output side with ordinary CoreAudio calls and apps record the input side.
// No IPC with coreaudiod's sandbox is needed.
//
// Written from Apple's AudioServerPlugIn.h documentation.

#include <CoreAudio/AudioServerPlugIn.h>
#include <CoreFoundation/CoreFoundation.h>
#include <mach/mach_time.h>
#include <pthread.h>
#include <stdatomic.h>
#include <string.h>

#define DEVICE_NAME "urdp Microphone"
#define DEVICE_UID "urdp-microphone"
#define MODEL_UID "urdp-microphone-model"
#define MANUFACTURER "urdp"

#define RATE 48000.0
#define CHANNELS 2
// Ring buffer length, also used as the zero time stamp period.
#define RING_FRAMES 16384

enum {
	kObjPlugIn = kAudioObjectPlugInObject,
	kObjDevice = 2,
	kObjInputStream = 3,
	kObjOutputStream = 4,
};

static AudioServerPlugInHostRef gHost;
static atomic_uint gRefCount;

static pthread_mutex_t gMutex = PTHREAD_MUTEX_INITIALIZER;
static UInt32 gIOCount;
static Float64 gHostTicksPerFrame;
static UInt64 gAnchorHostTime;
static UInt64 gTimeStampCount;

// Written and read only on the device's IO thread (or under gMutex with IO
// stopped). gWrittenEnd is one past the last sample time written; frames
// outside [gWrittenEnd-RING_FRAMES, gWrittenEnd) read as silence, so the
// input never repeats stale audio once urdp stops playing.
static Float32 gRing[RING_FRAMES * CHANNELS];
static SInt64 gWrittenEnd;

static AudioStreamBasicDescription stream_format(void) {
	AudioStreamBasicDescription f = {0};
	f.mSampleRate = RATE;
	f.mFormatID = kAudioFormatLinearPCM;
	f.mFormatFlags = kAudioFormatFlagIsFloat | kAudioFormatFlagsNativeEndian | kAudioFormatFlagIsPacked;
	f.mBytesPerPacket = 4 * CHANNELS;
	f.mFramesPerPacket = 1;
	f.mBytesPerFrame = 4 * CHANNELS;
	f.mChannelsPerFrame = CHANNELS;
	f.mBitsPerChannel = 32;
	return f;
}

// --- Properties -------------------------------------------------------------
//
// One function answers every property: it writes the value into buf (up to
// 512 bytes, enough for everything here) and reports its size, and whether
// it is an array that may be truncated.

typedef struct {
	UInt32 size;
	UInt32 elem; // element size for arrays, 0 for scalars
} PropInfo;

#define PUT(type, value)                       \
	do {                                       \
		*(type *)buf = (value);                \
		info->size = sizeof(type);             \
		return kAudioHardwareNoError;          \
	} while (0)

static OSStatus put_ids(void *buf, PropInfo *info, const AudioObjectID *ids, UInt32 n) {
	memcpy(buf, ids, n * sizeof *ids);
	info->size = n * sizeof *ids;
	info->elem = sizeof *ids;
	return kAudioHardwareNoError;
}

// Objects owned by obj, filtered by scope and by the class IDs in the
// qualifier, if any.
static OSStatus owned(AudioObjectID obj, AudioObjectPropertyScope scope, UInt32 qsize, const void *q,
		      void *buf, PropInfo *info) {
	AudioObjectID ids[2];
	UInt32 n = 0;
	if (obj == kObjPlugIn) {
		ids[n++] = kObjDevice;
	} else if (obj == kObjDevice) {
		if (scope != kAudioObjectPropertyScopeOutput)
			ids[n++] = kObjInputStream;
		if (scope != kAudioObjectPropertyScopeInput)
			ids[n++] = kObjOutputStream;
	}
	if (qsize >= sizeof(AudioClassID) && q) {
		const AudioClassID *classes = q;
		UInt32 nc = qsize / sizeof(AudioClassID), m = 0;
		for (UInt32 i = 0; i < n; i++) {
			AudioClassID c = ids[i] == kObjDevice ? kAudioDeviceClassID : kAudioStreamClassID;
			for (UInt32 j = 0; j < nc; j++) {
				if (classes[j] == c || classes[j] == kAudioObjectClassID) {
					ids[m++] = ids[i];
					break;
				}
			}
		}
		n = m;
	}
	return put_ids(buf, info, ids, n);
}

static OSStatus plugin_prop(const AudioObjectPropertyAddress *a, UInt32 qsize, const void *q, void *buf, PropInfo *info) {
	switch (a->mSelector) {
	case kAudioObjectPropertyBaseClass: PUT(AudioClassID, kAudioObjectClassID);
	case kAudioObjectPropertyClass: PUT(AudioClassID, kAudioPlugInClassID);
	case kAudioObjectPropertyOwner: PUT(AudioObjectID, kAudioObjectUnknown);
	case kAudioObjectPropertyManufacturer: PUT(CFStringRef, CFSTR(MANUFACTURER));
	case kAudioObjectPropertyOwnedObjects:
		return owned(kObjPlugIn, a->mScope, qsize, q, buf, info);
	case kAudioPlugInPropertyDeviceList: {
		AudioObjectID d = kObjDevice;
		return put_ids(buf, info, &d, 1);
	}
	case kAudioPlugInPropertyTranslateUIDToDevice: {
		AudioObjectID d = kAudioObjectUnknown;
		if (qsize == sizeof(CFStringRef) && q && *(CFStringRef const *)q &&
		    CFStringCompare(*(CFStringRef const *)q, CFSTR(DEVICE_UID), 0) == kCFCompareEqualTo)
			d = kObjDevice;
		PUT(AudioObjectID, d);
	}
	case kAudioPlugInPropertyResourceBundle: PUT(CFStringRef, CFSTR(""));
	}
	return kAudioHardwareUnknownPropertyError;
}

static OSStatus device_prop(const AudioObjectPropertyAddress *a, UInt32 qsize, const void *q, void *buf, PropInfo *info) {
	switch (a->mSelector) {
	case kAudioObjectPropertyBaseClass: PUT(AudioClassID, kAudioObjectClassID);
	case kAudioObjectPropertyClass: PUT(AudioClassID, kAudioDeviceClassID);
	case kAudioObjectPropertyOwner: PUT(AudioObjectID, kObjPlugIn);
	case kAudioObjectPropertyName: PUT(CFStringRef, CFSTR(DEVICE_NAME));
	case kAudioObjectPropertyManufacturer: PUT(CFStringRef, CFSTR(MANUFACTURER));
	case kAudioObjectPropertyOwnedObjects:
	case kAudioDevicePropertyStreams:
		return owned(kObjDevice, a->mScope, qsize, q, buf, info);
	case kAudioObjectPropertyControlList:
		return put_ids(buf, info, NULL, 0);
	case kAudioDevicePropertyDeviceUID: PUT(CFStringRef, CFSTR(DEVICE_UID));
	case kAudioDevicePropertyModelUID: PUT(CFStringRef, CFSTR(MODEL_UID));
	case kAudioDevicePropertyTransportType: PUT(UInt32, kAudioDeviceTransportTypeVirtual);
	case kAudioDevicePropertyRelatedDevices: {
		AudioObjectID d = kObjDevice;
		return put_ids(buf, info, &d, 1);
	}
	case kAudioDevicePropertyClockDomain: PUT(UInt32, 0);
	case kAudioDevicePropertyDeviceIsAlive: PUT(UInt32, 1);
	case kAudioDevicePropertyDeviceIsRunning: {
		pthread_mutex_lock(&gMutex);
		UInt32 running = gIOCount > 0;
		pthread_mutex_unlock(&gMutex);
		PUT(UInt32, running);
	}
	// Only the input side should be picked as a default device: the output
	// side exists for urdp to play into.
	case kAudioDevicePropertyDeviceCanBeDefaultDevice:
		PUT(UInt32, a->mScope == kAudioObjectPropertyScopeInput);
	case kAudioDevicePropertyDeviceCanBeDefaultSystemDevice: PUT(UInt32, 0);
	case kAudioDevicePropertyLatency: PUT(UInt32, 0);
	case kAudioDevicePropertySafetyOffset: PUT(UInt32, 0);
	case kAudioDevicePropertyNominalSampleRate: PUT(Float64, RATE);
	case kAudioDevicePropertyAvailableNominalSampleRates: {
		AudioValueRange r = {RATE, RATE};
		memcpy(buf, &r, sizeof r);
		info->size = info->elem = sizeof r;
		return kAudioHardwareNoError;
	}
	case kAudioDevicePropertyIsHidden: PUT(UInt32, 0);
	case kAudioDevicePropertyZeroTimeStampPeriod: PUT(UInt32, RING_FRAMES);
	case kAudioDevicePropertyClockIsStable: PUT(UInt32, 1);
	case kAudioDevicePropertyPreferredChannelsForStereo: {
		UInt32 ch[2] = {1, 2};
		memcpy(buf, ch, sizeof ch);
		info->size = sizeof ch;
		return kAudioHardwareNoError;
	}
	case kAudioDevicePropertyPreferredChannelLayout: {
		UInt32 size = offsetof(AudioChannelLayout, mChannelDescriptions) + CHANNELS * sizeof(AudioChannelDescription);
		AudioChannelLayout *l = buf;
		memset(l, 0, size);
		l->mChannelLayoutTag = kAudioChannelLayoutTag_UseChannelDescriptions;
		l->mNumberChannelDescriptions = CHANNELS;
		for (UInt32 i = 0; i < CHANNELS; i++)
			l->mChannelDescriptions[i].mChannelLabel = kAudioChannelLabel_Left + i;
		info->size = size;
		return kAudioHardwareNoError;
	}
	}
	return kAudioHardwareUnknownPropertyError;
}

static OSStatus stream_prop(AudioObjectID obj, const AudioObjectPropertyAddress *a, void *buf, PropInfo *info) {
	int input = obj == kObjInputStream;
	switch (a->mSelector) {
	case kAudioObjectPropertyBaseClass: PUT(AudioClassID, kAudioObjectClassID);
	case kAudioObjectPropertyClass: PUT(AudioClassID, kAudioStreamClassID);
	case kAudioObjectPropertyOwner: PUT(AudioObjectID, kObjDevice);
	case kAudioObjectPropertyOwnedObjects:
		return put_ids(buf, info, NULL, 0);
	case kAudioObjectPropertyName:
		PUT(CFStringRef, input ? CFSTR(DEVICE_NAME) : CFSTR(DEVICE_NAME " (urdp feed)"));
	case kAudioStreamPropertyIsActive: PUT(UInt32, 1);
	case kAudioStreamPropertyDirection: PUT(UInt32, input ? 1 : 0);
	case kAudioStreamPropertyTerminalType:
		PUT(UInt32, input ? kAudioStreamTerminalTypeMicrophone : kAudioStreamTerminalTypeLine);
	case kAudioStreamPropertyStartingChannel: PUT(UInt32, 1);
	case kAudioStreamPropertyLatency: PUT(UInt32, 0);
	case kAudioStreamPropertyVirtualFormat:
	case kAudioStreamPropertyPhysicalFormat:
		PUT(AudioStreamBasicDescription, stream_format());
	case kAudioStreamPropertyAvailableVirtualFormats:
	case kAudioStreamPropertyAvailablePhysicalFormats: {
		AudioStreamRangedDescription r;
		r.mFormat = stream_format();
		r.mSampleRateRange.mMinimum = r.mSampleRateRange.mMaximum = RATE;
		memcpy(buf, &r, sizeof r);
		info->size = info->elem = sizeof r;
		return kAudioHardwareNoError;
	}
	}
	return kAudioHardwareUnknownPropertyError;
}

static OSStatus get_prop(AudioObjectID obj, const AudioObjectPropertyAddress *a, UInt32 qsize, const void *q,
			 void *buf, PropInfo *info) {
	info->size = info->elem = 0;
	if (!a)
		return kAudioHardwareIllegalOperationError;
	switch (obj) {
	case kObjPlugIn: return plugin_prop(a, qsize, q, buf, info);
	case kObjDevice: return device_prop(a, qsize, q, buf, info);
	case kObjInputStream:
	case kObjOutputStream: return stream_prop(obj, a, buf, info);
	}
	return kAudioHardwareBadObjectError;
}

// --- Driver interface -------------------------------------------------------

static HRESULT drv_QueryInterface(void *inDriver, REFIID inUUID, LPVOID *outInterface);
static ULONG drv_AddRef(void *inDriver);
static ULONG drv_Release(void *inDriver);

static OSStatus drv_Initialize(AudioServerPlugInDriverRef d, AudioServerPlugInHostRef host) {
	gHost = host;
	mach_timebase_info_data_t tb;
	mach_timebase_info(&tb);
	gHostTicksPerFrame = (1e9 * tb.denom / tb.numer) / RATE;
	return kAudioHardwareNoError;
}

static OSStatus drv_CreateDevice(AudioServerPlugInDriverRef d, CFDictionaryRef desc,
				 const AudioServerPlugInClientInfo *client, AudioObjectID *outID) {
	return kAudioHardwareUnsupportedOperationError;
}

static OSStatus drv_DestroyDevice(AudioServerPlugInDriverRef d, AudioObjectID dev) {
	return kAudioHardwareUnsupportedOperationError;
}

static OSStatus drv_AddDeviceClient(AudioServerPlugInDriverRef d, AudioObjectID dev, const AudioServerPlugInClientInfo *c) {
	return dev == kObjDevice ? kAudioHardwareNoError : kAudioHardwareBadObjectError;
}

static OSStatus drv_RemoveDeviceClient(AudioServerPlugInDriverRef d, AudioObjectID dev, const AudioServerPlugInClientInfo *c) {
	return dev == kObjDevice ? kAudioHardwareNoError : kAudioHardwareBadObjectError;
}

static OSStatus drv_PerformConfigChange(AudioServerPlugInDriverRef d, AudioObjectID dev, UInt64 action, void *info) {
	return kAudioHardwareNoError;
}

static OSStatus drv_AbortConfigChange(AudioServerPlugInDriverRef d, AudioObjectID dev, UInt64 action, void *info) {
	return kAudioHardwareNoError;
}

static Boolean drv_HasProperty(AudioServerPlugInDriverRef d, AudioObjectID obj, pid_t pid,
			       const AudioObjectPropertyAddress *a) {
	UInt8 buf[512];
	PropInfo info;
	// TranslateUIDToDevice needs a qualifier; it exists regardless.
	CFStringRef none = NULL;
	return get_prop(obj, a, sizeof none, &none, buf, &info) == kAudioHardwareNoError;
}

static OSStatus drv_IsPropertySettable(AudioServerPlugInDriverRef d, AudioObjectID obj, pid_t pid,
				       const AudioObjectPropertyAddress *a, Boolean *outSettable) {
	if (!drv_HasProperty(d, obj, pid, a))
		return kAudioHardwareUnknownPropertyError;
	*outSettable = false;
	return kAudioHardwareNoError;
}

static OSStatus drv_GetPropertyDataSize(AudioServerPlugInDriverRef d, AudioObjectID obj, pid_t pid,
					const AudioObjectPropertyAddress *a, UInt32 qsize, const void *q,
					UInt32 *outSize) {
	UInt8 buf[512];
	PropInfo info;
	OSStatus err = get_prop(obj, a, qsize, q, buf, &info);
	if (err == kAudioHardwareNoError)
		*outSize = info.size;
	return err;
}

static OSStatus drv_GetPropertyData(AudioServerPlugInDriverRef d, AudioObjectID obj, pid_t pid,
				    const AudioObjectPropertyAddress *a, UInt32 qsize, const void *q,
				    UInt32 inSize, UInt32 *outSize, void *out) {
	UInt8 buf[512];
	PropInfo info;
	OSStatus err = get_prop(obj, a, qsize, q, buf, &info);
	if (err != kAudioHardwareNoError)
		return err;
	UInt32 n = info.size;
	if (inSize < n) {
		if (!info.elem)
			return kAudioHardwareBadPropertySizeError;
		n = inSize / info.elem * info.elem;
	}
	memcpy(out, buf, n);
	*outSize = n;
	return kAudioHardwareNoError;
}

static OSStatus drv_SetPropertyData(AudioServerPlugInDriverRef d, AudioObjectID obj, pid_t pid,
				    const AudioObjectPropertyAddress *a, UInt32 qsize, const void *q,
				    UInt32 inSize, const void *in) {
	if (!drv_HasProperty(d, obj, pid, a))
		return kAudioHardwareUnknownPropertyError;
	return kAudioHardwareUnsupportedOperationError;
}

static OSStatus drv_StartIO(AudioServerPlugInDriverRef d, AudioObjectID dev, UInt32 client) {
	if (dev != kObjDevice)
		return kAudioHardwareBadObjectError;
	pthread_mutex_lock(&gMutex);
	if (gIOCount++ == 0) {
		gAnchorHostTime = mach_absolute_time();
		gTimeStampCount = 0;
		gWrittenEnd = 0;
		memset(gRing, 0, sizeof gRing);
	}
	pthread_mutex_unlock(&gMutex);
	return kAudioHardwareNoError;
}

static OSStatus drv_StopIO(AudioServerPlugInDriverRef d, AudioObjectID dev, UInt32 client) {
	if (dev != kObjDevice)
		return kAudioHardwareBadObjectError;
	pthread_mutex_lock(&gMutex);
	if (gIOCount > 0)
		gIOCount--;
	pthread_mutex_unlock(&gMutex);
	return kAudioHardwareNoError;
}

// The device clock is the host clock: one zero time stamp every
// RING_FRAMES frames of host time.
static OSStatus drv_GetZeroTimeStamp(AudioServerPlugInDriverRef d, AudioObjectID dev, UInt32 client,
				     Float64 *outSampleTime, UInt64 *outHostTime, UInt64 *outSeed) {
	if (dev != kObjDevice)
		return kAudioHardwareBadObjectError;
	pthread_mutex_lock(&gMutex);
	Float64 ticksPerPeriod = gHostTicksPerFrame * RING_FRAMES;
	UInt64 now = mach_absolute_time();
	while (gAnchorHostTime + (UInt64)((gTimeStampCount + 1) * ticksPerPeriod) <= now)
		gTimeStampCount++;
	*outSampleTime = (Float64)gTimeStampCount * RING_FRAMES;
	*outHostTime = gAnchorHostTime + (UInt64)(gTimeStampCount * ticksPerPeriod);
	*outSeed = 1;
	pthread_mutex_unlock(&gMutex);
	return kAudioHardwareNoError;
}

static OSStatus drv_WillDoIOOperation(AudioServerPlugInDriverRef d, AudioObjectID dev, UInt32 client,
				      UInt32 op, Boolean *outWillDo, Boolean *outInPlace) {
	*outWillDo = op == kAudioServerPlugInIOOperationReadInput || op == kAudioServerPlugInIOOperationWriteMix;
	*outInPlace = true;
	return kAudioHardwareNoError;
}

static OSStatus drv_BeginIOOperation(AudioServerPlugInDriverRef d, AudioObjectID dev, UInt32 client,
				     UInt32 op, UInt32 frames, const AudioServerPlugInIOCycleInfo *cycle) {
	return kAudioHardwareNoError;
}

static void ring_write(SInt64 t, const Float32 *src, UInt32 frames) {
	if (t > gWrittenEnd) {
		// Clear the gap since the last write so it reads as silence.
		SInt64 from = gWrittenEnd > t - RING_FRAMES ? gWrittenEnd : t - RING_FRAMES;
		for (SInt64 i = from; i < t; i++)
			memset(&gRing[(i % RING_FRAMES) * CHANNELS], 0, sizeof(Float32) * CHANNELS);
	}
	for (UInt32 done = 0; done < frames;) {
		UInt32 pos = (UInt32)((t + done) % RING_FRAMES);
		UInt32 n = frames - done;
		if (n > RING_FRAMES - pos)
			n = RING_FRAMES - pos;
		memcpy(&gRing[pos * CHANNELS], src + done * CHANNELS, n * CHANNELS * sizeof(Float32));
		done += n;
	}
	if (t + frames > gWrittenEnd)
		gWrittenEnd = t + frames;
}

static void ring_read(SInt64 t, Float32 *dst, UInt32 frames) {
	for (UInt32 i = 0; i < frames; i++, t++) {
		Float32 *out = dst + i * CHANNELS;
		if (t < 0 || t >= gWrittenEnd || t < gWrittenEnd - RING_FRAMES) {
			memset(out, 0, sizeof(Float32) * CHANNELS);
		} else {
			memcpy(out, &gRing[(t % RING_FRAMES) * CHANNELS], sizeof(Float32) * CHANNELS);
		}
	}
}

static OSStatus drv_DoIOOperation(AudioServerPlugInDriverRef d, AudioObjectID dev, AudioObjectID stream,
				  UInt32 client, UInt32 op, UInt32 frames, const AudioServerPlugInIOCycleInfo *cycle,
				  void *main, void *secondary) {
	if (dev != kObjDevice)
		return kAudioHardwareBadObjectError;
	if (!main)
		return kAudioHardwareNoError;
	if (op == kAudioServerPlugInIOOperationWriteMix && stream == kObjOutputStream)
		ring_write((SInt64)cycle->mOutputTime.mSampleTime, main, frames);
	else if (op == kAudioServerPlugInIOOperationReadInput && stream == kObjInputStream)
		ring_read((SInt64)cycle->mInputTime.mSampleTime, main, frames);
	return kAudioHardwareNoError;
}

static OSStatus drv_EndIOOperation(AudioServerPlugInDriverRef d, AudioObjectID dev, UInt32 client,
				   UInt32 op, UInt32 frames, const AudioServerPlugInIOCycleInfo *cycle) {
	return kAudioHardwareNoError;
}

static AudioServerPlugInDriverInterface gInterface = {
	NULL,
	drv_QueryInterface,
	drv_AddRef,
	drv_Release,
	drv_Initialize,
	drv_CreateDevice,
	drv_DestroyDevice,
	drv_AddDeviceClient,
	drv_RemoveDeviceClient,
	drv_PerformConfigChange,
	drv_AbortConfigChange,
	drv_HasProperty,
	drv_IsPropertySettable,
	drv_GetPropertyDataSize,
	drv_GetPropertyData,
	drv_SetPropertyData,
	drv_StartIO,
	drv_StopIO,
	drv_GetZeroTimeStamp,
	drv_WillDoIOOperation,
	drv_BeginIOOperation,
	drv_DoIOOperation,
	drv_EndIOOperation,
};
static AudioServerPlugInDriverInterface *gInterfacePtr = &gInterface;
static AudioServerPlugInDriverRef gDriverRef = &gInterfacePtr;

static HRESULT drv_QueryInterface(void *inDriver, REFIID inUUID, LPVOID *outInterface) {
	if (inDriver != gDriverRef || !outInterface)
		return kAudioHardwareBadObjectError;
	CFUUIDRef req = CFUUIDCreateFromUUIDBytes(NULL, inUUID);
	HRESULT r = E_NOINTERFACE;
	if (req && (CFEqual(req, IUnknownUUID) || CFEqual(req, kAudioServerPlugInDriverInterfaceUUID))) {
		drv_AddRef(inDriver);
		*outInterface = gDriverRef;
		r = S_OK;
	}
	if (req)
		CFRelease(req);
	return r;
}

static ULONG drv_AddRef(void *inDriver) {
	return atomic_fetch_add(&gRefCount, 1) + 1;
}

static ULONG drv_Release(void *inDriver) {
	UInt32 n = atomic_load(&gRefCount);
	while (n > 0 && !atomic_compare_exchange_weak(&gRefCount, &n, n - 1)) {
	}
	return n > 0 ? n - 1 : 0;
}

// CFPlugIn factory named in Info.plist.
__attribute__((visibility("default"))) void *urdp_audio_create(CFAllocatorRef alloc, CFUUIDRef type) {
	if (!CFEqual(type, kAudioServerPlugInTypeUUID))
		return NULL;
	return gDriverRef;
}
