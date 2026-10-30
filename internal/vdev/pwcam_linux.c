// PipeWire virtual camera: a driver pw_stream that publishes a
// "Video/Source" node. libpipewire is loaded with dlopen so the binary
// still runs (and falls back to v4l2loopback) on hosts without it; only
// the headers are needed to build.

#include "pwcam_linux.h"

#include <dlfcn.h>
#include <errno.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include <pipewire/pipewire.h>
#include <spa/param/video/format-utils.h>
#include <spa/param/buffers.h>
#include <spa/pod/builder.h>

static struct {
	void (*init)(int *, char ***);
	struct pw_thread_loop *(*tl_new)(const char *, const struct spa_dict *);
	struct pw_loop *(*tl_get_loop)(struct pw_thread_loop *);
	int (*tl_start)(struct pw_thread_loop *);
	void (*tl_stop)(struct pw_thread_loop *);
	void (*tl_destroy)(struct pw_thread_loop *);
	void (*tl_lock)(struct pw_thread_loop *);
	void (*tl_unlock)(struct pw_thread_loop *);
	void (*tl_signal)(struct pw_thread_loop *, bool);
	int (*tl_timed_wait)(struct pw_thread_loop *, int);
	struct pw_context *(*ctx_new)(struct pw_loop *, struct pw_properties *, size_t);
	void (*ctx_destroy)(struct pw_context *);
	struct pw_core *(*ctx_connect)(struct pw_context *, struct pw_properties *, size_t);
	int (*core_disconnect)(struct pw_core *);
	struct pw_properties *(*props_new)(const char *, ...);
	struct pw_stream *(*stream_new)(struct pw_core *, const char *, struct pw_properties *);
	void (*stream_add_listener)(struct pw_stream *, struct spa_hook *, const struct pw_stream_events *, void *);
	int (*stream_connect)(struct pw_stream *, enum spa_direction, uint32_t, enum pw_stream_flags,
			      const struct spa_pod **, uint32_t);
	void (*stream_destroy)(struct pw_stream *);
	int (*stream_update_params)(struct pw_stream *, const struct spa_pod **, uint32_t);
	struct pw_buffer *(*stream_dequeue)(struct pw_stream *);
	int (*stream_queue)(struct pw_stream *, struct pw_buffer *);
	int (*stream_trigger)(struct pw_stream *);
	uint32_t (*stream_node_id)(struct pw_stream *);
} pw;

static pthread_once_t pw_once = PTHREAD_ONCE_INIT;
static char pw_load_err[256];

static void pw_load(void) {
	void *h = dlopen("libpipewire-0.3.so.0", RTLD_NOW | RTLD_LOCAL);
	if (!h) {
		snprintf(pw_load_err, sizeof pw_load_err, "libpipewire-0.3 not found: %s", dlerror());
		return;
	}
#define SYM(field, name)                                                             \
	if (!(*(void **)&pw.field = dlsym(h, name))) {                                   \
		snprintf(pw_load_err, sizeof pw_load_err, "libpipewire-0.3 lacks %s", name); \
		return;                                                                      \
	}
	SYM(init, "pw_init");
	SYM(tl_new, "pw_thread_loop_new");
	SYM(tl_get_loop, "pw_thread_loop_get_loop");
	SYM(tl_start, "pw_thread_loop_start");
	SYM(tl_stop, "pw_thread_loop_stop");
	SYM(tl_destroy, "pw_thread_loop_destroy");
	SYM(tl_lock, "pw_thread_loop_lock");
	SYM(tl_unlock, "pw_thread_loop_unlock");
	SYM(tl_signal, "pw_thread_loop_signal");
	SYM(tl_timed_wait, "pw_thread_loop_timed_wait");
	SYM(ctx_new, "pw_context_new");
	SYM(ctx_destroy, "pw_context_destroy");
	SYM(ctx_connect, "pw_context_connect");
	SYM(core_disconnect, "pw_core_disconnect");
	SYM(props_new, "pw_properties_new");
	SYM(stream_new, "pw_stream_new");
	SYM(stream_add_listener, "pw_stream_add_listener");
	SYM(stream_connect, "pw_stream_connect");
	SYM(stream_destroy, "pw_stream_destroy");
	SYM(stream_update_params, "pw_stream_update_params");
	SYM(stream_dequeue, "pw_stream_dequeue_buffer");
	SYM(stream_queue, "pw_stream_queue_buffer");
	SYM(stream_trigger, "pw_stream_trigger_process"); // 0.3.34+
	SYM(stream_node_id, "pw_stream_get_node_id");
#undef SYM
	pw.init(NULL, NULL);
}

struct pwcam {
	struct pw_thread_loop *loop;
	struct pw_context *ctx;
	struct pw_core *core;
	struct pw_stream *stream;
	struct spa_hook listener;

	enum pw_stream_state state;
	char err[256];

	// Advertised size, and the format the consumer picked (0 if none).
	int adv_w, adv_h;
	uint32_t fmt;
	int w, h;

	// Latest frame as tight I420, guarded by the loop lock.
	uint8_t *pend;
	size_t pend_cap;
	int pend_w, pend_h;
	int fresh;
	uint64_t frames; // frames handed to PipeWire
};

static const struct spa_pod *build_enum_format(struct spa_pod_builder *b, int w, int h) {
	// I420 first: it's what we have and what libwebrtc prefers. NV12 and
	// YUY2 are for consumers that only take those (OBS, some V4L2 shims).
	return spa_pod_builder_add_object(b,
		SPA_TYPE_OBJECT_Format, SPA_PARAM_EnumFormat,
		SPA_FORMAT_mediaType, SPA_POD_Id(SPA_MEDIA_TYPE_video),
		SPA_FORMAT_mediaSubtype, SPA_POD_Id(SPA_MEDIA_SUBTYPE_raw),
		SPA_FORMAT_VIDEO_format, SPA_POD_CHOICE_ENUM_Id(4,
			SPA_VIDEO_FORMAT_I420, SPA_VIDEO_FORMAT_I420,
			SPA_VIDEO_FORMAT_NV12, SPA_VIDEO_FORMAT_YUY2),
		SPA_FORMAT_VIDEO_size, SPA_POD_Rectangle(&SPA_RECTANGLE(w, h)),
		SPA_FORMAT_VIDEO_framerate, SPA_POD_Fraction(&SPA_FRACTION(30, 1)));
}

static int frame_size(uint32_t fmt, int w, int h, int *stride) {
	switch (fmt) {
	case SPA_VIDEO_FORMAT_YUY2:
		*stride = w * 2;
		return w * 2 * h;
	default: // I420, NV12: tightly packed planes in one block
		*stride = w;
		return w * h + 2 * (((w + 1) / 2) * ((h + 1) / 2));
	}
}

static void on_state_changed(void *data, enum pw_stream_state old, enum pw_stream_state state, const char *error) {
	struct pwcam *c = data;
	c->state = state;
	if (state == PW_STREAM_STATE_ERROR && error)
		snprintf(c->err, sizeof c->err, "%s", error);
	pw.tl_signal(c->loop, false);
}

static void on_param_changed(void *data, uint32_t id, const struct spa_pod *param) {
	struct pwcam *c = data;
	if (id != SPA_PARAM_Format)
		return;
	if (!param) {
		c->fmt = 0;
		c->w = c->h = 0;
		return;
	}
	struct spa_video_info_raw info;
	memset(&info, 0, sizeof info);
	if (spa_format_video_raw_parse(param, &info) < 0 || info.size.width == 0 || info.size.height == 0)
		return;
	c->fmt = info.format;
	c->w = info.size.width;
	c->h = info.size.height;
	int stride;
	int size = frame_size(c->fmt, c->w, c->h, &stride);
	uint8_t buf[512];
	struct spa_pod_builder b = SPA_POD_BUILDER_INIT(buf, sizeof buf);
	const struct spa_pod *params[2];
	params[0] = spa_pod_builder_add_object(&b,
		SPA_TYPE_OBJECT_ParamBuffers, SPA_PARAM_Buffers,
		SPA_PARAM_BUFFERS_buffers, SPA_POD_CHOICE_RANGE_Int(4, 2, 8),
		SPA_PARAM_BUFFERS_blocks, SPA_POD_Int(1),
		SPA_PARAM_BUFFERS_size, SPA_POD_Int(size),
		SPA_PARAM_BUFFERS_stride, SPA_POD_Int(stride),
		SPA_PARAM_BUFFERS_dataType, SPA_POD_CHOICE_FLAGS_Int((1 << SPA_DATA_MemFd) | (1 << SPA_DATA_MemPtr)));
	params[1] = spa_pod_builder_add_object(&b,
		SPA_TYPE_OBJECT_ParamMeta, SPA_PARAM_Meta,
		SPA_PARAM_META_type, SPA_POD_Id(SPA_META_Header),
		SPA_PARAM_META_size, SPA_POD_Int(sizeof(struct spa_meta_header)));
	pw.stream_update_params(c->stream, params, 2);
	// Show the newest frame in the new format straight away.
	if (c->pend_w)
		c->fresh = 1;
}

// Nearest-neighbour copy of a sw×sh plane (tight) into dw×dh with stride ds,
// taking every step-th byte of each destination row (for interleaving).
static void scale_plane(uint8_t *dst, int dw, int dh, int ds, int step,
			const uint8_t *src, int sw, int sh) {
	for (int y = 0; y < dh; y++) {
		const uint8_t *row = src + (size_t)(dh == sh ? y : (int)((int64_t)y * sh / dh)) * sw;
		uint8_t *out = dst + (size_t)y * ds;
		if (dw == sw && step == 1) {
			memcpy(out, row, dw);
			continue;
		}
		for (int x = 0; x < dw; x++)
			out[x * step] = row[dw == sw ? x : (int)((int64_t)x * sw / dw)];
	}
}

static void fill(struct pwcam *c, uint8_t *dst, int stride) {
	int sw = c->pend_w, sh = c->pend_h, scw = (sw + 1) / 2, sch = (sh + 1) / 2;
	const uint8_t *sy = c->pend, *su = sy + (size_t)sw * sh, *sv = su + (size_t)scw * sch;
	int w = c->w, h = c->h, cw = (w + 1) / 2, ch = (h + 1) / 2;
	switch (c->fmt) {
	case SPA_VIDEO_FORMAT_I420:
		scale_plane(dst, w, h, stride, 1, sy, sw, sh);
		scale_plane(dst + (size_t)stride * h, cw, ch, cw, 1, su, scw, sch);
		scale_plane(dst + (size_t)stride * h + (size_t)cw * ch, cw, ch, cw, 1, sv, scw, sch);
		break;
	case SPA_VIDEO_FORMAT_NV12:
		scale_plane(dst, w, h, stride, 1, sy, sw, sh);
		scale_plane(dst + (size_t)stride * h, cw, ch, cw * 2, 2, su, scw, sch);
		scale_plane(dst + (size_t)stride * h + 1, cw, ch, cw * 2, 2, sv, scw, sch);
		break;
	case SPA_VIDEO_FORMAT_YUY2:
		scale_plane(dst, w, h, stride, 2, sy, sw, sh);
		// Chroma rows are shared by two luma rows.
		for (int y = 0; y < h; y++) {
			int r = (int)((int64_t)(y / 2) * sch / ch);
			scale_plane(dst + (size_t)y * stride + 1, cw, 1, 0, 4, su + (size_t)r * scw, scw, 1);
			scale_plane(dst + (size_t)y * stride + 3, cw, 1, 0, 4, sv + (size_t)r * scw, scw, 1);
		}
		break;
	}
}

static void on_process(void *data) {
	struct pwcam *c = data;
	if (!c->fresh || !c->fmt)
		return;
	struct pw_buffer *b = pw.stream_dequeue(c->stream);
	if (!b)
		return;
	struct spa_buffer *buf = b->buffer;
	struct spa_data *d = &buf->datas[0];
	int stride;
	int size = frame_size(c->fmt, c->w, c->h, &stride);
	if (!d->data || d->maxsize < (uint32_t)size) {
		d->chunk->size = 0;
		pw.stream_queue(c->stream, b);
		return;
	}
	fill(c, d->data, stride);
	d->chunk->offset = 0;
	d->chunk->size = size;
	d->chunk->stride = stride;
	struct spa_meta_header *hdr = spa_buffer_find_meta_data(buf, SPA_META_Header, sizeof *hdr);
	if (hdr) {
		struct timespec ts;
		clock_gettime(CLOCK_MONOTONIC, &ts);
		hdr->pts = (int64_t)ts.tv_sec * SPA_NSEC_PER_SEC + ts.tv_nsec;
		hdr->flags = 0;
		hdr->seq = c->frames;
		hdr->dts_offset = 0;
	}
	c->fresh = 0;
	c->frames++;
	pw.stream_queue(c->stream, b);
}

static const struct pw_stream_events stream_events = {
	PW_VERSION_STREAM_EVENTS,
	.state_changed = on_state_changed,
	.param_changed = on_param_changed,
	.process = on_process,
};

static void pwcam_free(struct pwcam *c) {
	if (c->loop)
		pw.tl_stop(c->loop);
	if (c->stream)
		pw.stream_destroy(c->stream);
	if (c->core)
		pw.core_disconnect(c->core);
	if (c->ctx)
		pw.ctx_destroy(c->ctx);
	if (c->loop)
		pw.tl_destroy(c->loop);
	free(c->pend);
	free(c);
}

struct pwcam *pwcam_open(const char *name, const char *desc, char *err, int errlen) {
	pthread_once(&pw_once, pw_load);
	if (pw_load_err[0]) {
		snprintf(err, errlen, "%s", pw_load_err);
		return NULL;
	}
	struct pwcam *c = calloc(1, sizeof *c);
	if (!c) {
		snprintf(err, errlen, "out of memory");
		return NULL;
	}
	c->adv_w = 640;
	c->adv_h = 480;
	c->loop = pw.tl_new("urdp-camera", NULL);
	if (!c->loop || !(c->ctx = pw.ctx_new(pw.tl_get_loop(c->loop), NULL, 0))) {
		snprintf(err, errlen, "creating PipeWire context failed");
		goto fail;
	}
	if (pw.tl_start(c->loop) < 0) {
		snprintf(err, errlen, "starting PipeWire thread failed");
		goto fail;
	}
	pw.tl_lock(c->loop);
	c->core = pw.ctx_connect(c->ctx, NULL, 0);
	if (!c->core) {
		snprintf(err, errlen, "no PipeWire daemon reachable: %s", strerror(errno));
		pw.tl_unlock(c->loop);
		goto fail;
	}
	c->stream = pw.stream_new(c->core, name,
		pw.props_new(
			PW_KEY_MEDIA_TYPE, "Video",
			PW_KEY_MEDIA_CATEGORY, "Capture",
			PW_KEY_MEDIA_ROLE, "Camera",
			PW_KEY_MEDIA_CLASS, "Video/Source",
			PW_KEY_NODE_NAME, name,
			PW_KEY_NODE_NICK, desc,
			PW_KEY_NODE_DESCRIPTION, desc,
			PW_KEY_APP_NAME, "urdp",
			NULL));
	if (!c->stream) {
		snprintf(err, errlen, "creating PipeWire stream failed: %s", strerror(errno));
		pw.tl_unlock(c->loop);
		goto fail;
	}
	pw.stream_add_listener(c->stream, &c->listener, &stream_events, c);
	uint8_t buf[512];
	struct spa_pod_builder b = SPA_POD_BUILDER_INIT(buf, sizeof buf);
	const struct spa_pod *params[1] = {build_enum_format(&b, c->adv_w, c->adv_h)};
	int r = pw.stream_connect(c->stream, SPA_DIRECTION_OUTPUT, PW_ID_ANY,
		PW_STREAM_FLAG_DRIVER | PW_STREAM_FLAG_MAP_BUFFERS, params, 1);
	if (r < 0) {
		snprintf(err, errlen, "connecting PipeWire stream failed: %s", strerror(-r));
		pw.tl_unlock(c->loop);
		goto fail;
	}
	// Wait until the node exists so it's listed as soon as we return.
	while (c->state == PW_STREAM_STATE_CONNECTING || c->state == PW_STREAM_STATE_UNCONNECTED) {
		if (pw.tl_timed_wait(c->loop, 5) != 0) {
			snprintf(err, errlen, "PipeWire stream did not connect");
			pw.tl_unlock(c->loop);
			goto fail;
		}
	}
	if (c->state == PW_STREAM_STATE_ERROR) {
		snprintf(err, errlen, "PipeWire stream failed: %s", c->err);
		pw.tl_unlock(c->loop);
		goto fail;
	}
	pw.tl_unlock(c->loop);
	return c;
fail:
	pwcam_free(c);
	return NULL;
}

int pwcam_write(struct pwcam *c, const uint8_t *y, int ys, const uint8_t *u, int us,
		const uint8_t *v, int vs, int w, int h, char *err, int errlen) {
	int cw = (w + 1) / 2, ch = (h + 1) / 2;
	size_t need = (size_t)w * h + 2 * (size_t)cw * ch;
	pw.tl_lock(c->loop);
	if (c->state == PW_STREAM_STATE_ERROR || c->state == PW_STREAM_STATE_UNCONNECTED) {
		snprintf(err, errlen, "PipeWire stream %s: %s",
			c->state == PW_STREAM_STATE_ERROR ? "failed" : "disconnected", c->err);
		pw.tl_unlock(c->loop);
		return -1;
	}
	if (need > c->pend_cap) {
		uint8_t *p = realloc(c->pend, need);
		if (!p) {
			pw.tl_unlock(c->loop);
			snprintf(err, errlen, "out of memory");
			return -1;
		}
		c->pend = p;
		c->pend_cap = need;
	}
	uint8_t *dy = c->pend, *du = dy + (size_t)w * h, *dv = du + (size_t)cw * ch;
	for (int r = 0; r < h; r++)
		memcpy(dy + (size_t)r * w, y + (size_t)r * ys, w);
	for (int r = 0; r < ch; r++) {
		memcpy(du + (size_t)r * cw, u + (size_t)r * us, cw);
		memcpy(dv + (size_t)r * cw, v + (size_t)r * vs, cw);
	}
	c->pend_w = w;
	c->pend_h = h;
	c->fresh = 1;
	if (w != c->adv_w || h != c->adv_h) {
		// Offer only the new size; the consumer renegotiates and we
		// scale into the old size until it has.
		c->adv_w = w;
		c->adv_h = h;
		uint8_t buf[512];
		struct spa_pod_builder b = SPA_POD_BUILDER_INIT(buf, sizeof buf);
		const struct spa_pod *params[1] = {build_enum_format(&b, w, h)};
		pw.stream_update_params(c->stream, params, 1);
	}
	pw.stream_trigger(c->stream);
	pw.tl_unlock(c->loop);
	return 0;
}

void pwcam_stats(struct pwcam *c, int *w, int *h, uint32_t *fmt, uint64_t *frames, uint32_t *node) {
	pw.tl_lock(c->loop);
	*w = c->w;
	*h = c->h;
	*fmt = c->fmt;
	*frames = c->frames;
	*node = pw.stream_node_id(c->stream);
	pw.tl_unlock(c->loop);
}

void pwcam_close(struct pwcam *c) {
	pwcam_free(c);
}
