#import <AVFoundation/AVFoundation.h>

// Whether this process may record audio inputs (TCC "Microphone"). Without
// it CoreAudio delivers silence from every input, including ours.
int urdp_mic_authorized(void) {
	return [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio] == AVAuthorizationStatusAuthorized;
}
