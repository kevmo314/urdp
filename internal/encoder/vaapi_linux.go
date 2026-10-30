package encoder

// Hardware H.264 and HEVC through VA-API (Intel iHD/i965, AMD/Mesa
// radeonsi, Mesa d3d12, ...). libva is loaded with dlopen, so binaries
// built with this still start on machines without it; the backend then
// fails fast and the software encoders take over.
//
// The stream is IDR + P frames only, each P referencing the previous frame,
// with the low-power (VDEnc/VCN fixed-function) entrypoint when the driver
// has one. Rate control is CBR with a 100ms HRD buffer and frame skipping
// disabled, falling back to VBR or CQP. Sequence/picture headers (and slice
// headers when the driver accepts them) are written here as packed headers,
// as FFmpeg does, so the bitstream doesn't depend on each driver's header
// writer. Every Encode waits for its frame: one in, one out.

/*
#cgo CFLAGS: -O2
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <va/va.h>
#include <va/va_drm.h>
#include <va/va_enc_h264.h>
#include <va/va_enc_hevc.h>

// ---- libva, loaded at run time ----

static struct {
	int loaded;
	VADisplay (*GetDisplayDRM)(int);
	VAStatus (*Initialize)(VADisplay, int *, int *);
	VAStatus (*Terminate)(VADisplay);
	VAMessageCallback (*SetInfoCallback)(VADisplay, VAMessageCallback, void *);
	int (*MaxNumEntrypoints)(VADisplay);
	VAStatus (*QueryConfigEntrypoints)(VADisplay, VAProfile, VAEntrypoint *, int *);
	VAStatus (*GetConfigAttributes)(VADisplay, VAProfile, VAEntrypoint, VAConfigAttrib *, int);
	VAStatus (*CreateConfig)(VADisplay, VAProfile, VAEntrypoint, VAConfigAttrib *, int, VAConfigID *);
	VAStatus (*DestroyConfig)(VADisplay, VAConfigID);
	VAStatus (*CreateSurfaces)(VADisplay, unsigned, unsigned, unsigned, VASurfaceID *, unsigned, VASurfaceAttrib *, unsigned);
	VAStatus (*DestroySurfaces)(VADisplay, VASurfaceID *, int);
	VAStatus (*CreateContext)(VADisplay, VAConfigID, int, int, int, VASurfaceID *, int, VAContextID *);
	VAStatus (*DestroyContext)(VADisplay, VAContextID);
	VAStatus (*CreateBuffer)(VADisplay, VAContextID, VABufferType, unsigned, unsigned, void *, VABufferID *);
	VAStatus (*DestroyBuffer)(VADisplay, VABufferID);
	VAStatus (*MapBuffer)(VADisplay, VABufferID, void **);
	VAStatus (*UnmapBuffer)(VADisplay, VABufferID);
	VAStatus (*BeginPicture)(VADisplay, VAContextID, VASurfaceID);
	VAStatus (*RenderPicture)(VADisplay, VAContextID, VABufferID *, int);
	VAStatus (*EndPicture)(VADisplay, VAContextID);
	VAStatus (*SyncSurface)(VADisplay, VASurfaceID);
	VAStatus (*DeriveImage)(VADisplay, VASurfaceID, VAImage *);
	VAStatus (*CreateImage)(VADisplay, VAImageFormat *, int, int, VAImage *);
	VAStatus (*DestroyImage)(VADisplay, VAImageID);
	VAStatus (*PutImage)(VADisplay, VASurfaceID, VAImageID, int, int, unsigned, unsigned, int, int, unsigned, unsigned);
	const char *(*ErrorStr)(VAStatus);
} va;

static const char *va_load(void) {
	if (va.loaded) return NULL;
	void *l = dlopen("libva.so.2", RTLD_NOW | RTLD_GLOBAL);
	if (!l) return "libva.so.2 not found";
	void *d = dlopen("libva-drm.so.2", RTLD_NOW | RTLD_GLOBAL);
	if (!d) return "libva-drm.so.2 not found";
#define SYM(lib, name) if (!(*(void **)&va.name = dlsym(lib, "va" #name))) return "libva lacks va" #name;
	SYM(d, GetDisplayDRM)
	SYM(l, Initialize) SYM(l, Terminate) SYM(l, MaxNumEntrypoints) SYM(l, QueryConfigEntrypoints)
	SYM(l, GetConfigAttributes) SYM(l, CreateConfig) SYM(l, DestroyConfig) SYM(l, CreateSurfaces)
	SYM(l, DestroySurfaces) SYM(l, CreateContext) SYM(l, DestroyContext) SYM(l, CreateBuffer)
	SYM(l, DestroyBuffer) SYM(l, MapBuffer) SYM(l, UnmapBuffer) SYM(l, BeginPicture)
	SYM(l, RenderPicture) SYM(l, EndPicture) SYM(l, SyncSurface) SYM(l, DeriveImage)
	SYM(l, CreateImage) SYM(l, DestroyImage) SYM(l, PutImage) SYM(l, ErrorStr)
#undef SYM
	*(void **)&va.SetInfoCallback = dlsym(l, "vaSetInfoCallback"); // optional
	va.loaded = 1;
	return NULL;
}

// ---- bit writer for packed headers ----

typedef struct { uint8_t buf[512]; int bits; } bw;

static void put(bw *w, uint32_t v, int n) {
	for (int i = n - 1; i >= 0; i--) {
		if (w->bits >= (int)sizeof(w->buf) * 8) return;
		if ((v >> i) & 1) w->buf[w->bits >> 3] |= 0x80 >> (w->bits & 7);
		w->bits++;
	}
}
static void ue(bw *w, uint32_t v) {
	v++;
	int n = 0;
	for (uint32_t t = v; t > 1; t >>= 1) n++;
	put(w, 0, n);
	put(w, v, n + 1);
}
static void se(bw *w, int32_t v) { ue(w, v <= 0 ? (uint32_t)(-2 * v) : (uint32_t)(2 * v - 1)); }
static void trailing(bw *w) { put(w, 1, 1); while (w->bits & 7) put(w, 0, 1); }

// Packs a NAL (start code + header bytes already in w) inserting emulation
// prevention bytes after the header; returns the length in bits. Slice
// headers end mid-byte, so the bit length is kept exact.
static int escape(const bw *w, int hdr_bytes, uint8_t *out) {
	int nbytes = (w->bits + 7) / 8, o = 0, zeros = 0;
	for (int i = 0; i < nbytes; i++) {
		uint8_t b = w->buf[i];
		if (i >= hdr_bytes) {
			if (zeros >= 2 && b <= 3) { out[o++] = 3; zeros = 0; }
			zeros = b == 0 ? zeros + 1 : 0;
		}
		out[o++] = b;
	}
	return o * 8 - (nbytes * 8 - w->bits);
}

// ---- encoder ----

enum { CODEC_H264, CODEC_HEVC };

typedef struct {
	int codec;
	int fd;
	VADisplay dpy;
	VAConfigID cfg;
	VAContextID ctx;
	VASurfaceID input, recon[2];
	VABufferID coded;
	int w, h, cw, ch; // visible and coded sizes
	int fps, kbps, rc_mode, packed, lowpower;
	int profile_idc, level_idc;
	// HEVC block sizes and tools
	int log2_min_cb, log2_ctb, log2_min_tb, log2_max_tb, depth_inter, depth_intra;
	int amp, sao, strong_intra, cu_qp_delta, transform_skip;
	VAImage img; int derived;
	uint64_t since_idr; int cur; int idr_id; int rate_dirty;
	uint8_t *out; size_t out_len, out_cap;
	char err[256];
} vaenc;

#define CHECK(call, what) do { VAStatus s_ = (call); if (s_ != VA_STATUS_SUCCESS) { \
	snprintf(e->err, sizeof e->err, "%s: %s", what, va.ErrorStr(s_)); return -1; } } while (0)

static int has_entrypoint(VADisplay dpy, VAProfile p, VAEntrypoint want) {
	int n = va.MaxNumEntrypoints(dpy), got = 0;
	VAEntrypoint *eps = calloc(n > 0 ? n : 1, sizeof *eps);
	int ok = 0;
	if (va.QueryConfigEntrypoints(dpy, p, eps, &got) == VA_STATUS_SUCCESS)
		for (int i = 0; i < got; i++) ok |= eps[i] == want;
	free(eps);
	return ok;
}

static int open_display(vaenc *e, const char *dev) {
	e->fd = open(dev, O_RDWR | O_CLOEXEC);
	if (e->fd < 0) { snprintf(e->err, sizeof e->err, "open %s: %s", dev, strerror(errno)); return -1; }
	e->dpy = va.GetDisplayDRM(e->fd);
	if (!e->dpy) { snprintf(e->err, sizeof e->err, "vaGetDisplayDRM failed"); return -1; }
	if (va.SetInfoCallback) va.SetInfoCallback(e->dpy, NULL, NULL);
	int major, minor;
	CHECK(va.Initialize(e->dpy, &major, &minor), "vaInitialize");
	return 0;
}

static void vaenc_close(vaenc *e) {
	if (e->dpy) {
		if (e->derived == 2) va.DestroyImage(e->dpy, e->img.image_id);
		if (e->coded != VA_INVALID_ID) va.DestroyBuffer(e->dpy, e->coded);
		if (e->ctx != VA_INVALID_ID) va.DestroyContext(e->dpy, e->ctx);
		VASurfaceID s[3] = {e->input, e->recon[0], e->recon[1]};
		if (e->input != VA_INVALID_SURFACE) va.DestroySurfaces(e->dpy, s, 3);
		if (e->cfg != VA_INVALID_ID) va.DestroyConfig(e->dpy, e->cfg);
		va.Terminate(e->dpy);
	}
	if (e->fd >= 0) close(e->fd);
	free(e->out);
	free(e);
}

// Chooses the profile and entrypoint, rate control and packed headers.
static int setup_config(vaenc *e) {
	VAProfile cands[3];
	int nc = 0;
	if (e->codec == CODEC_H264) {
		// Constrained baseline is what the SDP advertises; Main/High are
		// used with the same baseline tools (CAVLC, no B, no 8x8).
		cands[nc++] = VAProfileH264ConstrainedBaseline;
		cands[nc++] = VAProfileH264Main;
		cands[nc++] = VAProfileH264High;
	} else {
		cands[nc++] = VAProfileHEVCMain;
	}
	VAProfile prof = VAProfileNone;
	VAEntrypoint ep = 0;
	for (int i = 0; i < nc && prof == VAProfileNone; i++) {
		if (has_entrypoint(e->dpy, cands[i], VAEntrypointEncSliceLP)) { prof = cands[i]; ep = VAEntrypointEncSliceLP; }
		else if (has_entrypoint(e->dpy, cands[i], VAEntrypointEncSlice)) { prof = cands[i]; ep = VAEntrypointEncSlice; }
	}
	if (prof == VAProfileNone) {
		snprintf(e->err, sizeof e->err, "driver has no %s encode entrypoint", e->codec == CODEC_H264 ? "H.264" : "HEVC");
		return -1;
	}
	e->lowpower = ep == VAEntrypointEncSliceLP;
	if (e->codec == CODEC_H264)
		e->profile_idc = prof == VAProfileH264ConstrainedBaseline ? 66 : prof == VAProfileH264Main ? 77 : 100;

	VAConfigAttrib a[5] = {
		{.type = VAConfigAttribRTFormat}, {.type = VAConfigAttribRateControl},
		{.type = VAConfigAttribEncPackedHeaders}, {.type = VAConfigAttribEncHEVCBlockSizes},
		{.type = VAConfigAttribEncHEVCFeatures},
	};
	CHECK(va.GetConfigAttributes(e->dpy, prof, ep, a, e->codec == CODEC_HEVC ? 5 : 3), "vaGetConfigAttributes");
	if (a[0].value == VA_ATTRIB_NOT_SUPPORTED || !(a[0].value & VA_RT_FORMAT_YUV420)) {
		snprintf(e->err, sizeof e->err, "driver can't encode YUV 4:2:0");
		return -1;
	}
	uint32_t rc = a[1].value == VA_ATTRIB_NOT_SUPPORTED ? VA_RC_CQP : a[1].value;
	e->rc_mode = rc & VA_RC_CBR ? VA_RC_CBR : rc & VA_RC_VBR ? VA_RC_VBR : VA_RC_CQP;
	e->packed = 0;
	if (a[2].value != VA_ATTRIB_NOT_SUPPORTED)
		e->packed = a[2].value & (VA_ENC_PACKED_HEADER_SEQUENCE | VA_ENC_PACKED_HEADER_PICTURE | VA_ENC_PACKED_HEADER_SLICE);

	if (e->codec == CODEC_HEVC) {
		// Defaults as FFmpeg uses without the block size attribute.
		e->log2_min_cb = 3; e->log2_ctb = 5; e->log2_min_tb = 2; e->log2_max_tb = 5;
		e->depth_inter = 3; e->depth_intra = 3;
		if (a[3].value != VA_ATTRIB_NOT_SUPPORTED) {
			VAConfigAttribValEncHEVCBlockSizes b = {.value = a[3].value};
			e->log2_ctb = b.bits.log2_max_coding_tree_block_size_minus3 + 3;
			e->log2_min_cb = b.bits.log2_min_luma_coding_block_size_minus3 + 3;
			e->log2_min_tb = b.bits.log2_min_luma_transform_block_size_minus2 + 2;
			e->log2_max_tb = b.bits.log2_max_luma_transform_block_size_minus2 + 2;
			e->depth_inter = b.bits.max_max_transform_hierarchy_depth_inter;
			e->depth_intra = b.bits.max_max_transform_hierarchy_depth_intra;
		}
		e->amp = 1; e->sao = 0; e->strong_intra = 0; e->transform_skip = 0;
		e->cu_qp_delta = e->rc_mode != VA_RC_CQP;
		if (a[4].value != VA_ATTRIB_NOT_SUPPORTED) {
			VAConfigAttribValEncHEVCFeatures f = {.value = a[4].value};
			e->amp = f.bits.amp != 0;
			e->sao = f.bits.sao == 2; // only when the driver insists
			e->strong_intra = f.bits.strong_intra_smoothing != 0;
			e->transform_skip = f.bits.transform_skip != 0; // helps text
			e->cu_qp_delta = f.bits.cu_qp_delta == 2 || (f.bits.cu_qp_delta && e->rc_mode != VA_RC_CQP);
		}
	}

	VAConfigAttrib set[3] = {
		{.type = VAConfigAttribRTFormat, .value = VA_RT_FORMAT_YUV420},
		{.type = VAConfigAttribRateControl, .value = e->rc_mode},
		{.type = VAConfigAttribEncPackedHeaders, .value = e->packed},
	};
	int nset = a[2].value != VA_ATTRIB_NOT_SUPPORTED ? 3 : 2;
	CHECK(va.CreateConfig(e->dpy, prof, ep, set, nset, &e->cfg), "vaCreateConfig");
	return 0;
}

static vaenc *vaenc_open(int codec, const char *dev, int w, int h, int fps, int kbps, char *err, int errlen) {
	vaenc *e = calloc(1, sizeof *e);
	e->fd = -1;
	e->cfg = e->ctx = e->coded = VA_INVALID_ID;
	e->input = e->recon[0] = e->recon[1] = VA_INVALID_SURFACE;
	e->codec = codec; e->w = w; e->h = h; e->fps = fps; e->kbps = kbps;
	const char *lerr = va_load();
	if (lerr) { snprintf(err, errlen, "%s", lerr); free(e); return NULL; }
	if (open_display(e, dev) < 0 || setup_config(e) < 0) goto fail;
	e->cw = (w + 15) & ~15;
	e->ch = (h + 15) & ~15;
	if (codec == CODEC_HEVC) {
		int m = 1 << e->log2_min_cb;
		if (m > 16) { e->cw = (w + m - 1) & ~(m - 1); e->ch = (h + m - 1) & ~(m - 1); }
	}
	e->level_idc = codec == CODEC_H264 ? (e->cw * e->ch > 2048 * 1088 ? 52 : 42)
		: (e->cw * e->ch > 2228224 ? 156 : 123); // HEVC: 5.2 or 4.1, times 30
	VASurfaceID s[3];
	if (va.CreateSurfaces(e->dpy, VA_RT_FORMAT_YUV420, e->cw, e->ch, s, 3, NULL, 0) != VA_STATUS_SUCCESS) {
		snprintf(e->err, sizeof e->err, "vaCreateSurfaces failed");
		goto fail;
	}
	e->input = s[0]; e->recon[0] = s[1]; e->recon[1] = s[2];
	if (va.CreateContext(e->dpy, e->cfg, e->cw, e->ch, VA_PROGRESSIVE, s, 3, &e->ctx) != VA_STATUS_SUCCESS) {
		snprintf(e->err, sizeof e->err, "vaCreateContext failed");
		goto fail;
	}
	if (va.CreateBuffer(e->dpy, e->ctx, VAEncCodedBufferType, e->cw * e->ch * 3 / 2 + 65536, 1, NULL, &e->coded) != VA_STATUS_SUCCESS) {
		snprintf(e->err, sizeof e->err, "coded buffer allocation failed");
		goto fail;
	}
	// Uploading through a derived image writes straight into the surface;
	// drivers that can't derive get vaPutImage from a separate image.
	VAImage probe;
	VAStatus ds = va.DeriveImage(e->dpy, e->input, &probe);
	if (ds == VA_STATUS_SUCCESS) {
		if (probe.format.fourcc == VA_FOURCC_NV12) e->derived = 1;
		va.DestroyImage(e->dpy, probe.image_id);
	}
	if (!e->derived) {
		VAImageFormat f = {.fourcc = VA_FOURCC_NV12, .byte_order = VA_LSB_FIRST, .bits_per_pixel = 12};
		if (va.CreateImage(e->dpy, &f, e->cw, e->ch, &e->img) != VA_STATUS_SUCCESS) {
			snprintf(e->err, sizeof e->err, "no NV12 upload path");
			goto fail;
		}
		e->derived = 2;
	}
	e->rate_dirty = 1;
	return e;
fail:
	snprintf(err, errlen, "%s", e->err);
	vaenc_close(e);
	return NULL;
}

static const char *vaenc_info(vaenc *e) {
	static __thread char s[96];
	snprintf(s, sizeof s, "%s%s, %s, packed headers %#x", e->lowpower ? "low-power" : "full",
		e->codec == CODEC_H264 ? (e->profile_idc == 66 ? " CB" : e->profile_idc == 77 ? " Main" : " High") : " Main",
		e->rc_mode == VA_RC_CBR ? "CBR" : e->rc_mode == VA_RC_VBR ? "VBR" : "CQP", e->packed);
	return s;
}

// Copies the NV12 picture into the input surface. A derived image is
// only held while copying, as some drivers can't encode a surface that
// has one outstanding.
static int vaenc_upload(vaenc *e, const uint8_t *y, int ys, const uint8_t *uv, int uvs) {
	uint8_t *p;
	if (e->derived == 1) CHECK(va.DeriveImage(e->dpy, e->input, &e->img), "vaDeriveImage");
	VAStatus ms = va.MapBuffer(e->dpy, e->img.buf, (void **)&p);
	if (ms != VA_STATUS_SUCCESS) {
		if (e->derived == 1) va.DestroyImage(e->dpy, e->img.image_id);
		snprintf(e->err, sizeof e->err, "vaMapBuffer(image): %s", va.ErrorStr(ms));
		return -1;
	}
	for (int j = 0; j < e->h; j++)
		memcpy(p + e->img.offsets[0] + (size_t)j * e->img.pitches[0], y + (size_t)j * ys, e->w);
	for (int j = 0; j < e->h / 2; j++)
		memcpy(p + e->img.offsets[1] + (size_t)j * e->img.pitches[1], uv + (size_t)j * uvs, e->w);
	va.UnmapBuffer(e->dpy, e->img.buf);
	if (e->derived == 1) va.DestroyImage(e->dpy, e->img.image_id);
	if (e->derived == 2)
		CHECK(va.PutImage(e->dpy, e->input, e->img.image_id, 0, 0, e->cw, e->ch, 0, 0, e->cw, e->ch), "vaPutImage");
	return 0;
}

static int add_buf(vaenc *e, VABufferID *ids, int *n, VABufferType t, unsigned size, void *data) {
	CHECK(va.CreateBuffer(e->dpy, e->ctx, t, size, 1, data, &ids[*n]), "vaCreateBuffer");
	(*n)++;
	return 0;
}

static int add_packed(vaenc *e, VABufferID *ids, int *n, int type, const bw *w, int hdr_bytes) {
	uint8_t data[640];
	int bits = escape(w, hdr_bytes, data);
	VAEncPackedHeaderParameterBuffer p = {.type = type, .bit_length = bits, .has_emulation_bytes = 1};
	if (add_buf(e, ids, n, VAEncPackedHeaderParameterBufferType, sizeof p, &p) < 0) return -1;
	return add_buf(e, ids, n, VAEncPackedHeaderDataBufferType, (bits + 7) / 8, data);
}

static int add_misc(vaenc *e, VABufferID *ids, int *n, VAEncMiscParameterType type, const void *data, size_t size) {
	uint8_t buf[sizeof(VAEncMiscParameterBuffer) + 256] = {0};
	VAEncMiscParameterBuffer *m = (VAEncMiscParameterBuffer *)buf;
	m->type = type;
	memcpy(m->data, data, size);
	return add_buf(e, ids, n, VAEncMiscParameterBufferType, sizeof *m + size, buf);
}

static int add_rate(vaenc *e, VABufferID *ids, int *n) {
	if (e->rc_mode == VA_RC_CQP) return 0;
	uint32_t bps = e->kbps * 1000;
	VAEncMiscParameterRateControl rc = {
		.bits_per_second = bps, .target_percentage = e->rc_mode == VA_RC_VBR ? 90 : 100,
		.window_size = 100, .initial_qp = 26, .min_qp = 10, .max_qp = 51,
	};
	rc.rc_flags.bits.disable_frame_skip = 1; // one in, one out
	// A 100ms buffer bounds bursts (keyframes) like x264's VBV.
	VAEncMiscParameterHRD hrd = {.initial_buffer_fullness = bps / 20, .buffer_size = bps / 10};
	VAEncMiscParameterFrameRate fr = {.framerate = e->fps};
	if (add_misc(e, ids, n, VAEncMiscParameterTypeRateControl, &rc, sizeof rc) < 0) return -1;
	if (add_misc(e, ids, n, VAEncMiscParameterTypeHRD, &hrd, sizeof hrd) < 0) return -1;
	return add_misc(e, ids, n, VAEncMiscParameterTypeFrameRate, &fr, sizeof fr);
}

// ---- H.264 ----

#define H264_LOG2_MAX_FRAME_NUM 8

static void h264_nal(bw *w, int ref_idc, int type) {
	put(w, 1, 32);
	put(w, 0, 1); put(w, ref_idc, 2); put(w, type, 5);
}

static void h264_sps(vaenc *e, bw *w) {
	h264_nal(w, 3, 7);
	put(w, e->profile_idc, 8);
	put(w, e->profile_idc == 66, 1); // constraint_set0
	put(w, e->profile_idc <= 77, 1); // constraint_set1: decodable as main/CB
	put(w, 0, 6);
	put(w, e->level_idc, 8);
	ue(w, 0); // sps id
	if (e->profile_idc == 100) {
		ue(w, 1); ue(w, 0); ue(w, 0); put(w, 0, 1); put(w, 0, 1);
	}
	ue(w, H264_LOG2_MAX_FRAME_NUM - 4);
	ue(w, 2); // pic_order_cnt_type: output order = decode order
	ue(w, 1); // max_num_ref_frames
	put(w, 0, 1);
	ue(w, e->cw / 16 - 1);
	ue(w, e->ch / 16 - 1);
	put(w, 1, 1); // frame_mbs_only
	put(w, 1, 1); // direct_8x8_inference
	int crop = e->cw != e->w || e->ch != e->h;
	put(w, crop, 1);
	if (crop) { ue(w, 0); ue(w, (e->cw - e->w) / 2); ue(w, 0); ue(w, (e->ch - e->h) / 2); }
	put(w, 1, 1); // vui
	put(w, 0, 1); put(w, 0, 1); // aspect ratio, overscan
	put(w, 1, 1); put(w, 5, 3); put(w, 0, 1); // video signal: unspecified format, limited range
	put(w, 1, 1); put(w, 1, 8); put(w, 1, 8); put(w, 1, 8); // BT.709
	put(w, 0, 1); put(w, 0, 1); put(w, 0, 1); put(w, 0, 1); put(w, 0, 1); // chroma loc, timing, hrd x2, pic_struct
	put(w, 1, 1); // bitstream_restriction: no reordering, so decoders output at once
	put(w, 1, 1); ue(w, 0); ue(w, 0); ue(w, 16); ue(w, 16);
	ue(w, 0); // max_num_reorder_frames
	ue(w, 1); // max_dec_frame_buffering
	trailing(w);
}

static void h264_pps(vaenc *e, bw *w) {
	h264_nal(w, 3, 8);
	ue(w, 0); ue(w, 0);
	put(w, 0, 1); // CAVLC
	put(w, 0, 1);
	ue(w, 0); // slice groups
	ue(w, 0); ue(w, 0); // ref idx defaults
	put(w, 0, 1); put(w, 0, 2);
	se(w, 0); se(w, 0); se(w, 0); // init qp/qs - 26, chroma qp offset
	put(w, 1, 1); // deblocking_filter_control_present
	put(w, 0, 1); put(w, 0, 1);
	trailing(w);
}

static void h264_slice_header(vaenc *e, bw *w, int idr, int frame_num) {
	h264_nal(w, 3, idr ? 5 : 1);
	ue(w, 0);           // first_mb_in_slice
	ue(w, idr ? 7 : 5); // I or P, all slices alike
	ue(w, 0);           // pps id
	put(w, frame_num, H264_LOG2_MAX_FRAME_NUM);
	if (idr) ue(w, e->idr_id);
	if (!idr) { put(w, 0, 1); put(w, 0, 1); } // num_ref_idx override, ref list modification
	if (idr) { put(w, 0, 1); put(w, 0, 1); } else put(w, 0, 1); // dec_ref_pic_marking
	se(w, 0);   // slice_qp_delta
	ue(w, 0);   // deblocking on
	se(w, 0); se(w, 0);
}

static void h264_invalid(VAPictureH264 *p) {
	p->picture_id = VA_INVALID_SURFACE;
	p->flags = VA_PICTURE_H264_INVALID;
}

static int h264_frame(vaenc *e, int idr, VABufferID *ids, int *n) {
	int frame_num = e->since_idr % (1 << H264_LOG2_MAX_FRAME_NUM);
	VASurfaceID cur = e->recon[e->cur], ref = e->recon[e->cur ^ 1];
	if (idr) {
		VAEncSequenceParameterBufferH264 sp = {
			.seq_parameter_set_id = 0, .level_idc = e->level_idc,
			.intra_period = 0, .intra_idr_period = 0, .ip_period = 1,
			.bits_per_second = e->rc_mode == VA_RC_CQP ? 0 : e->kbps * 1000,
			.max_num_ref_frames = 1,
			.picture_width_in_mbs = e->cw / 16, .picture_height_in_mbs = e->ch / 16,
			.frame_cropping_flag = e->cw != e->w || e->ch != e->h,
			.frame_crop_right_offset = (e->cw - e->w) / 2, .frame_crop_bottom_offset = (e->ch - e->h) / 2,
			.vui_parameters_present_flag = 1,
		};
		sp.seq_fields.bits.chroma_format_idc = 1;
		sp.seq_fields.bits.frame_mbs_only_flag = 1;
		sp.seq_fields.bits.direct_8x8_inference_flag = 1;
		sp.seq_fields.bits.log2_max_frame_num_minus4 = H264_LOG2_MAX_FRAME_NUM - 4;
		sp.seq_fields.bits.pic_order_cnt_type = 2;
		sp.vui_fields.bits.bitstream_restriction_flag = 1;
		sp.vui_fields.bits.motion_vectors_over_pic_boundaries_flag = 1;
		sp.vui_fields.bits.log2_max_mv_length_horizontal = 16;
		sp.vui_fields.bits.log2_max_mv_length_vertical = 16;
		if (add_buf(e, ids, n, VAEncSequenceParameterBufferType, sizeof sp, &sp) < 0) return -1;
	}
	VAEncPictureParameterBufferH264 pp = {
		.coded_buf = e->coded, .frame_num = frame_num, .pic_init_qp = 26,
		.pic_parameter_set_id = 0, .seq_parameter_set_id = 0,
	};
	pp.CurrPic.picture_id = cur;
	pp.CurrPic.frame_idx = frame_num;
	pp.CurrPic.TopFieldOrderCnt = pp.CurrPic.BottomFieldOrderCnt = 2 * (int32_t)e->since_idr;
	for (int i = 0; i < 16; i++) h264_invalid(&pp.ReferenceFrames[i]);
	VAPictureH264 r = {0};
	if (!idr) {
		r.picture_id = ref;
		r.frame_idx = (e->since_idr - 1) % (1 << H264_LOG2_MAX_FRAME_NUM);
		r.flags = VA_PICTURE_H264_SHORT_TERM_REFERENCE;
		r.TopFieldOrderCnt = r.BottomFieldOrderCnt = 2 * (int32_t)(e->since_idr - 1);
		pp.ReferenceFrames[0] = r;
	}
	pp.pic_fields.bits.idr_pic_flag = idr;
	pp.pic_fields.bits.reference_pic_flag = 1;
	pp.pic_fields.bits.deblocking_filter_control_present_flag = 1;
	if (add_buf(e, ids, n, VAEncPictureParameterBufferType, sizeof pp, &pp) < 0) return -1;

	if (idr && (e->packed & VA_ENC_PACKED_HEADER_SEQUENCE)) {
		bw w = {0};
		h264_sps(e, &w);
		if (add_packed(e, ids, n, VAEncPackedHeaderSequence, &w, 5) < 0) return -1;
	}
	if (idr && (e->packed & VA_ENC_PACKED_HEADER_PICTURE)) {
		bw w = {0};
		h264_pps(e, &w);
		if (add_packed(e, ids, n, VAEncPackedHeaderPicture, &w, 5) < 0) return -1;
	}
	if (e->packed & VA_ENC_PACKED_HEADER_SLICE) {
		bw w = {0};
		h264_slice_header(e, &w, idr, frame_num);
		if (add_packed(e, ids, n, VAEncPackedHeaderSlice, &w, 5) < 0) return -1;
	}

	VAEncSliceParameterBufferH264 sl = {
		.macroblock_address = 0, .num_macroblocks = (e->cw / 16) * (e->ch / 16),
		.slice_type = idr ? 2 : 0, .pic_parameter_set_id = 0, .idr_pic_id = e->idr_id,
		.num_ref_idx_l0_active_minus1 = 0,
	};
	for (int i = 0; i < 32; i++) { h264_invalid(&sl.RefPicList0[i]); h264_invalid(&sl.RefPicList1[i]); }
	if (!idr) sl.RefPicList0[0] = r;
	return add_buf(e, ids, n, VAEncSliceParameterBufferType, sizeof sl, &sl);
}

// ---- HEVC ----

#define HEVC_LOG2_MAX_POC_LSB 12 // as FFmpeg; drivers writing their own slice headers assume it

static void hevc_nal(bw *w, int type) {
	put(w, 1, 32);
	put(w, 0, 1); put(w, type, 6); put(w, 0, 6); put(w, 1, 3);
}

static void hevc_ptl(vaenc *e, bw *w) {
	put(w, 0, 2); put(w, 0, 1); put(w, 1, 5); // Main, tier 0
	put(w, 0x60000000, 32);                    // compatible with Main and Main10
	put(w, 1, 1); put(w, 0, 1); put(w, 0, 1); put(w, 1, 1); // progressive, frame only
	put(w, 0, 32); put(w, 0, 11);               // 43 reserved bits
	put(w, 0, 1);
	put(w, e->level_idc, 8);
}

static void hevc_vps(vaenc *e, bw *w) {
	hevc_nal(w, 32);
	put(w, 0, 4); put(w, 1, 1); put(w, 1, 1); put(w, 0, 6); put(w, 0, 3); put(w, 1, 1);
	put(w, 0xffff, 16);
	hevc_ptl(e, w);
	put(w, 1, 1); ue(w, 1); ue(w, 0); ue(w, 0); // ordering: 2 pictures, no reorder
	put(w, 0, 6); ue(w, 0);
	put(w, 0, 1); put(w, 0, 1);
	trailing(w);
}

static void hevc_sps(vaenc *e, bw *w) {
	hevc_nal(w, 33);
	put(w, 0, 4); put(w, 0, 3); put(w, 1, 1);
	hevc_ptl(e, w);
	ue(w, 0); ue(w, 1); // sps id, 4:2:0
	ue(w, e->cw); ue(w, e->ch);
	int crop = e->cw != e->w || e->ch != e->h;
	put(w, crop, 1);
	if (crop) { ue(w, 0); ue(w, (e->cw - e->w) / 2); ue(w, 0); ue(w, (e->ch - e->h) / 2); }
	ue(w, 0); ue(w, 0); // 8-bit
	ue(w, HEVC_LOG2_MAX_POC_LSB - 4);
	put(w, 1, 1); ue(w, 1); ue(w, 0); ue(w, 0);
	ue(w, e->log2_min_cb - 3); ue(w, e->log2_ctb - e->log2_min_cb);
	ue(w, e->log2_min_tb - 2); ue(w, e->log2_max_tb - e->log2_min_tb);
	ue(w, e->depth_inter); ue(w, e->depth_intra);
	put(w, 0, 1);              // scaling lists
	put(w, e->amp, 1);
	put(w, e->sao, 1);
	put(w, 0, 1);              // pcm
	ue(w, 0);                  // short-term RPS are sent in slice headers
	put(w, 0, 1);              // long-term refs
	put(w, 0, 1);              // temporal mvp
	put(w, e->strong_intra, 1);
	put(w, 1, 1);              // vui
	put(w, 0, 1); put(w, 0, 1); // aspect, overscan
	put(w, 1, 1); put(w, 5, 3); put(w, 0, 1); put(w, 1, 1); put(w, 1, 8); put(w, 1, 8); put(w, 1, 8);
	put(w, 0, 1); put(w, 0, 1); put(w, 0, 1); put(w, 0, 1); put(w, 0, 1); put(w, 0, 1); put(w, 0, 1);
	put(w, 0, 1);              // sps extension
	trailing(w);
}

static void hevc_pps(vaenc *e, bw *w) {
	hevc_nal(w, 34);
	ue(w, 0); ue(w, 0);
	put(w, 0, 1); put(w, 0, 1); put(w, 0, 3); put(w, 0, 1); put(w, 0, 1);
	ue(w, 0); ue(w, 0);
	se(w, 0);                  // init_qp - 26
	put(w, 0, 1);
	put(w, e->transform_skip, 1);
	put(w, e->cu_qp_delta, 1);
	if (e->cu_qp_delta) ue(w, 0);
	se(w, 0); se(w, 0);
	put(w, 0, 1); put(w, 0, 1); put(w, 0, 1); put(w, 0, 1); // chroma offsets, weighted, transquant bypass
	put(w, 0, 1); put(w, 0, 1); // tiles, wavefronts
	put(w, 0, 1);              // loop filter across slices
	put(w, 0, 1);              // deblocking control
	put(w, 0, 1); put(w, 0, 1); // scaling list, list modification
	ue(w, 0);
	put(w, 0, 1); put(w, 0, 1);
	trailing(w);
}

static void hevc_slice_header(vaenc *e, bw *w, int idr) {
	hevc_nal(w, idr ? 19 : 1); // IDR_W_RADL or TRAIL_R
	put(w, 1, 1);              // first slice segment
	if (idr) put(w, 0, 1);     // no_output_of_prior_pics
	ue(w, 0);                  // pps id
	ue(w, idr ? 2 : 1);        // I or P
	if (!idr) {
		put(w, e->since_idr % (1u << HEVC_LOG2_MAX_POC_LSB), HEVC_LOG2_MAX_POC_LSB);
		put(w, 0, 1);          // RPS in the header:
		ue(w, 1); ue(w, 0);    // one negative picture,
		ue(w, 0); put(w, 1, 1); // the previous one, used
	}
	if (e->sao) { put(w, 1, 1); put(w, 1, 1); }
	if (!idr) {
		put(w, 0, 1);          // num_ref_idx override
		ue(w, 0);              // five_minus_max_num_merge_cand
	}
	se(w, 0);                  // slice_qp_delta
	put(w, 1, 1); while (w->bits & 7) put(w, 0, 1); // byte_alignment()
}

static void hevc_invalid(VAPictureHEVC *p) {
	p->picture_id = VA_INVALID_SURFACE;
	p->flags = VA_PICTURE_HEVC_INVALID;
}

static int hevc_frame(vaenc *e, int idr, VABufferID *ids, int *n) {
	VASurfaceID cur = e->recon[e->cur], ref = e->recon[e->cur ^ 1];
	if (idr) {
		VAEncSequenceParameterBufferHEVC sp = {
			.general_profile_idc = 1, .general_level_idc = e->level_idc, .general_tier_flag = 0,
			.intra_period = 0, .intra_idr_period = 0, .ip_period = 1,
			.bits_per_second = e->rc_mode == VA_RC_CQP ? 0 : e->kbps * 1000,
			.pic_width_in_luma_samples = e->cw, .pic_height_in_luma_samples = e->ch,
			.log2_min_luma_coding_block_size_minus3 = e->log2_min_cb - 3,
			.log2_diff_max_min_luma_coding_block_size = e->log2_ctb - e->log2_min_cb,
			.log2_min_transform_block_size_minus2 = e->log2_min_tb - 2,
			.log2_diff_max_min_transform_block_size = e->log2_max_tb - e->log2_min_tb,
			.max_transform_hierarchy_depth_inter = e->depth_inter,
			.max_transform_hierarchy_depth_intra = e->depth_intra,
			.vui_parameters_present_flag = 1,
		};
		sp.seq_fields.bits.chroma_format_idc = 1;
		sp.seq_fields.bits.amp_enabled_flag = e->amp;
		sp.seq_fields.bits.sample_adaptive_offset_enabled_flag = e->sao;
		sp.seq_fields.bits.strong_intra_smoothing_enabled_flag = e->strong_intra;
		sp.seq_fields.bits.low_delay_seq = 1;
		if (add_buf(e, ids, n, VAEncSequenceParameterBufferType, sizeof sp, &sp) < 0) return -1;
	}
	VAEncPictureParameterBufferHEVC pp = {
		.coded_buf = e->coded, .collocated_ref_pic_index = 0xff, .pic_init_qp = 26,
		.nal_unit_type = idr ? 19 : 1,
	};
	pp.decoded_curr_pic.picture_id = cur;
	pp.decoded_curr_pic.pic_order_cnt = e->since_idr;
	for (int i = 0; i < 15; i++) hevc_invalid(&pp.reference_frames[i]);
	VAPictureHEVC r = {0};
	if (!idr) {
		r.picture_id = ref;
		r.pic_order_cnt = e->since_idr - 1;
		r.flags = VA_PICTURE_HEVC_RPS_ST_CURR_BEFORE;
		pp.reference_frames[0] = r;
	}
	pp.pic_fields.bits.idr_pic_flag = idr;
	pp.pic_fields.bits.coding_type = idr ? 1 : 2;
	pp.pic_fields.bits.reference_pic_flag = 1;
	pp.pic_fields.bits.transform_skip_enabled_flag = e->transform_skip;
	pp.pic_fields.bits.cu_qp_delta_enabled_flag = e->cu_qp_delta;
	if (add_buf(e, ids, n, VAEncPictureParameterBufferType, sizeof pp, &pp) < 0) return -1;

	if (idr && (e->packed & VA_ENC_PACKED_HEADER_SEQUENCE)) {
		bw v = {0}, s = {0};
		hevc_vps(e, &v);
		hevc_sps(e, &s);
		if (add_packed(e, ids, n, VAEncPackedHeaderSequence, &v, 6) < 0) return -1;
		if (add_packed(e, ids, n, VAEncPackedHeaderSequence, &s, 6) < 0) return -1;
	}
	if (idr && (e->packed & VA_ENC_PACKED_HEADER_PICTURE)) {
		bw w = {0};
		hevc_pps(e, &w);
		if (add_packed(e, ids, n, VAEncPackedHeaderPicture, &w, 6) < 0) return -1;
	}
	if (e->packed & VA_ENC_PACKED_HEADER_SLICE) {
		bw w = {0};
		hevc_slice_header(e, &w, idr);
		if (add_packed(e, ids, n, VAEncPackedHeaderSlice, &w, 6) < 0) return -1;
	}

	int ctb = 1 << e->log2_ctb;
	VAEncSliceParameterBufferHEVC sl = {
		.slice_segment_address = 0,
		.num_ctu_in_slice = ((e->cw + ctb - 1) / ctb) * ((e->ch + ctb - 1) / ctb),
		.slice_type = idr ? 2 : 1, .slice_pic_parameter_set_id = 0,
		.max_num_merge_cand = 5,
	};
	for (int i = 0; i < 15; i++) { hevc_invalid(&sl.ref_pic_list0[i]); hevc_invalid(&sl.ref_pic_list1[i]); }
	if (!idr) sl.ref_pic_list0[0] = r;
	sl.slice_fields.bits.last_slice_of_pic_flag = 1;
	sl.slice_fields.bits.slice_sao_luma_flag = e->sao;
	sl.slice_fields.bits.slice_sao_chroma_flag = e->sao;
	sl.slice_fields.bits.collocated_from_l0_flag = 1;
	return add_buf(e, ids, n, VAEncSliceParameterBufferType, sizeof sl, &sl);
}

// Writes the packed headers a frame would carry, as an Annex B stream with
// a dummy slice payload, for tests to check without a device.
static int vaenc_test_headers(int codec, int w, int h, int sao, int idr, int since_idr, uint8_t *out) {
	vaenc e = {.codec = codec, .w = w, .h = h, .cw = (w + 15) & ~15, .ch = (h + 15) & ~15,
		.profile_idc = 66, .level_idc = codec == CODEC_H264 ? 42 : 123, .idr_id = 1,
		.log2_min_cb = 3, .log2_ctb = 5, .log2_min_tb = 2, .log2_max_tb = 5, .depth_inter = 3, .depth_intra = 3,
		.amp = 1, .sao = sao, .strong_intra = 1, .cu_qp_delta = 1, .transform_skip = 1, .since_idr = since_idr};
	int hb = codec == CODEC_H264 ? 5 : 6, n = 0;
	bw ws[4] = {0};
	int nw = 0;
	if (codec == CODEC_H264) {
		if (idr) { h264_sps(&e, &ws[nw++]); h264_pps(&e, &ws[nw++]); }
		h264_slice_header(&e, &ws[nw++], idr, since_idr % (1 << H264_LOG2_MAX_FRAME_NUM));
	} else {
		if (idr) { hevc_vps(&e, &ws[nw++]); hevc_sps(&e, &ws[nw++]); hevc_pps(&e, &ws[nw++]); }
		hevc_slice_header(&e, &ws[nw++], idr);
	}
	for (int i = 0; i < nw; i++) {
		if (i == nw - 1) { // stand-in slice data
			if (ws[i].bits & 7) put(&ws[i], 0, 8 - (ws[i].bits & 7));
			put(&ws[i], 0xa5a5a5a5, 32); put(&ws[i], 0x80, 8);
		}
		n += (escape(&ws[i], hb, out + n) + 7) / 8;
	}
	return n;
}

// ---- per frame ----

static int vaenc_encode(vaenc *e, int idr) {
	VABufferID ids[24];
	int n = 0, rc = -1;
	if (idr) { e->since_idr = 0; e->idr_id = (e->idr_id + 1) & 0xffff; }
	if ((e->codec == CODEC_H264 ? h264_frame(e, idr, ids, &n) : hevc_frame(e, idr, ids, &n)) < 0) goto out;
	if ((idr || e->rate_dirty) && add_rate(e, ids, &n) < 0) goto out;
	e->rate_dirty = 0;
	VAStatus s;
	if ((s = va.BeginPicture(e->dpy, e->ctx, e->input)) != VA_STATUS_SUCCESS) {
		snprintf(e->err, sizeof e->err, "vaBeginPicture: %s", va.ErrorStr(s)); goto out;
	}
	if ((s = va.RenderPicture(e->dpy, e->ctx, ids, n)) != VA_STATUS_SUCCESS) {
		va.EndPicture(e->dpy, e->ctx);
		snprintf(e->err, sizeof e->err, "vaRenderPicture: %s", va.ErrorStr(s)); goto out;
	}
	if ((s = va.EndPicture(e->dpy, e->ctx)) != VA_STATUS_SUCCESS) {
		snprintf(e->err, sizeof e->err, "vaEndPicture: %s", va.ErrorStr(s)); goto out;
	}
	if ((s = va.SyncSurface(e->dpy, e->input)) != VA_STATUS_SUCCESS) {
		snprintf(e->err, sizeof e->err, "vaSyncSurface: %s", va.ErrorStr(s)); goto out;
	}
	VACodedBufferSegment *seg;
	if ((s = va.MapBuffer(e->dpy, e->coded, (void **)&seg)) != VA_STATUS_SUCCESS) {
		snprintf(e->err, sizeof e->err, "vaMapBuffer(coded): %s", va.ErrorStr(s)); goto out;
	}
	e->out_len = 0;
	for (; seg; seg = seg->next) {
		if (e->out_len + seg->size > e->out_cap) {
			e->out_cap = (e->out_len + seg->size) * 2;
			e->out = realloc(e->out, e->out_cap);
		}
		memcpy(e->out + e->out_len, seg->buf, seg->size);
		e->out_len += seg->size;
	}
	va.UnmapBuffer(e->dpy, e->coded);
	e->since_idr++;
	e->cur ^= 1; // this frame's reconstruction is the next one's reference
	rc = 0;
out:
	for (int i = 0; i < n; i++) va.DestroyBuffer(e->dpy, ids[i]);
	return rc;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"log"
	"os"
	"unsafe"

	"github.com/kevmo314/urdp/internal/capture"
)

func init() {
	// Experimental: never run on a real GPU yet (see README).
	Register(Backend{Name: "vaapi-h264", Codec: H264, Hardware: true, Experimental: true, New: func(w, h int, o Options) (Encoder, error) { return newVAAPI(H264, w, h, o) }})
	Register(Backend{Name: "vaapi-hevc", Codec: H265, Hardware: true, Experimental: true, New: func(w, h int, o Options) (Encoder, error) { return newVAAPI(H265, w, h, o) }})
}

// vaapiDevice is the DRM render node to use: $URDP_VAAPI_DEVICE, else the
// first render node.
func vaapiDevice() (string, error) {
	if d := os.Getenv("URDP_VAAPI_DEVICE"); d != "" {
		return d, nil
	}
	for i := 128; i < 136; i++ {
		d := fmt.Sprintf("/dev/dri/renderD%d", i)
		if _, err := os.Stat(d); err == nil {
			return d, nil
		}
	}
	return "", errors.New("no DRM render node (/dev/dri/renderD*)")
}

type vaapiEncoder struct {
	e      *C.vaenc
	codec  Codec
	pic    *NV12
	w, h   int
	keyint int64
	frames int64
	lastKF int64
	buf    []byte
}

func newVAAPI(c Codec, w, h int, o Options) (*vaapiEncoder, error) {
	w, h, o, err := normalize(w, h, o)
	if err != nil {
		return nil, err
	}
	dev, err := vaapiDevice()
	if err != nil {
		return nil, err
	}
	codec := C.int(C.CODEC_H264)
	if c == H265 {
		codec = C.CODEC_HEVC
	}
	cdev := C.CString(dev)
	defer C.free(unsafe.Pointer(cdev))
	var cerr [256]C.char
	e := C.vaenc_open(codec, cdev, C.int(w), C.int(h), C.int(o.FPS), C.int(o.BitrateKbps), &cerr[0], C.int(len(cerr)))
	if e == nil {
		return nil, fmt.Errorf("vaapi %s: %s", dev, C.GoString(&cerr[0]))
	}
	log.Printf("encoder: VA-API %s on %s (%s)", c, dev, C.GoString(C.vaenc_info(e)))
	return &vaapiEncoder{e: e, codec: c, pic: NewNV12(w, h), w: w, h: h, keyint: int64(o.FPS * o.KeyintSeconds)}, nil
}

func (e *vaapiEncoder) Codec() Codec     { return e.codec }
func (e *vaapiEncoder) Size() (int, int) { return e.w, e.h }

// SetBitrate takes effect from the next frame.
func (e *vaapiEncoder) SetBitrate(kbps int) error {
	e.e.kbps = C.int(kbps)
	e.e.rate_dirty = 1
	return nil
}

// Encode returns Annex B NAL units for one frame, valid until the next call.
func (e *vaapiEncoder) Encode(f *capture.Frame, _ int64, keyframe bool) ([]byte, bool, error) {
	if f.Width < e.w || f.Height < e.h {
		return nil, false, errors.New("encoder: frame smaller than encoder")
	}
	e.pic.Convert(f)
	if C.vaenc_upload(e.e, (*C.uint8_t)(unsafe.Pointer(&e.pic.Y[0])), C.int(e.pic.YStride),
		(*C.uint8_t)(unsafe.Pointer(&e.pic.UV[0])), C.int(e.pic.UVStride)) < 0 {
		return nil, false, fmt.Errorf("vaapi: %s", C.GoString(&e.e.err[0]))
	}
	if e.frames == 0 || e.frames-e.lastKF >= e.keyint {
		keyframe = true
	}
	if C.vaenc_encode(e.e, cbool(keyframe)) < 0 {
		return nil, false, fmt.Errorf("vaapi: %s", C.GoString(&e.e.err[0]))
	}
	if keyframe {
		e.lastKF = e.frames
	}
	e.frames++
	if e.e.out_len == 0 {
		return nil, false, nil
	}
	e.buf = append(e.buf[:0], unsafe.Slice((*byte)(unsafe.Pointer(e.e.out)), int(e.e.out_len))...)
	return e.buf, keyframe, nil
}

// vaapiTestHeaders exposes the packed header writers to tests.
func vaapiTestHeaders(c Codec, w, h int, sao, idr bool, sinceIDR int) []byte {
	codec := C.int(C.CODEC_H264)
	if c == H265 {
		codec = C.CODEC_HEVC
	}
	buf := (*C.uint8_t)(C.malloc(4096))
	defer C.free(unsafe.Pointer(buf))
	n := C.vaenc_test_headers(codec, C.int(w), C.int(h), cbool(sao), cbool(idr), C.int(sinceIDR), buf)
	return C.GoBytes(unsafe.Pointer(buf), n)
}

func (e *vaapiEncoder) Close() {
	if e.e != nil {
		C.vaenc_close(e.e)
		e.e = nil
	}
}
