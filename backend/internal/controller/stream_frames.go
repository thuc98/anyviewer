package controller

import (
	"image"
	"time"
)

type screenChange struct {
	version uint64
	rect    image.Rectangle
}
type streamSnapshot struct {
	pixels        *image.RGBA
	rect          image.Rectangle
	width, height int
	version       uint64
	full          bool
	settings      viewerSettings
}
type streamStats struct {
	Transport string  `json:"transport"`
	Encoding  string  `json:"encoding"`
	FPS       float64 `json:"fps"`
	TargetFPS int     `json:"target_fps"`
	Quality   int     `json:"quality"`
	Bitrate   int     `json:"bitrate_kbps"`
	RTT       float64 `json:"rtt_ms"`
	EncodeMS  float64 `json:"encode_ms"`
	Dropped   uint64  `json:"dropped_frames"`
	Error     string  `json:"error,omitempty"`
}

func (v *Viewer) recordScreenLocked(pixels *image.RGBA, dirty image.Rectangle) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.screenImage = pixels
	v.frame = nil
	v.state.Frames++
	v.screenVersion++
	v.screenChanges = append(v.screenChanges, screenChange{v.screenVersion, dirty})
	if len(v.screenChanges) > 64 {
		copy(v.screenChanges, v.screenChanges[len(v.screenChanges)-64:])
		v.screenChanges = v.screenChanges[:64]
	}
}

func (v *Viewer) snapshotScreen(after uint64, full bool) streamSnapshot {
	v.screenMu.RLock()
	defer v.screenMu.RUnlock()
	v.mu.Lock()
	defer v.mu.Unlock()
	result := streamSnapshot{version: v.screenVersion, settings: v.settings}
	source := v.screenImage
	if source == nil {
		return result
	}
	result.width, result.height = source.Rect.Dx(), source.Rect.Dy()
	if after == 0 || len(v.screenChanges) == 0 || after+1 < v.screenChanges[0].version {
		full = true
	}
	if after == v.screenVersion && !full {
		return result
	}
	rect := image.Rectangle{}
	if full {
		rect = source.Rect
	} else {
		for _, change := range v.screenChanges {
			if change.version > after {
				rect = rect.Union(change.rect)
			}
		}
	}
	rect = rect.Intersect(source.Rect)
	if rect.Empty() {
		return result
	}
	if rect.Dx()*rect.Dy()*4 >= source.Rect.Dx()*source.Rect.Dy()*3 {
		full = true
		rect = source.Rect
	}
	result.full, result.rect = full, rect
	result.pixels = cropImage(source, rect.Min.X, rect.Min.Y, rect.Dx(), rect.Dy())
	return result
}

type adaptiveStream struct {
	fps, quality, bitrate int
	last                  time.Time
	healthy               int
}

func newAdaptiveStream(settings viewerSettings) adaptiveStream {
	return adaptiveStream{fps: settings.StreamFPS, quality: settings.StreamQuality, bitrate: settings.StreamBitrate}
}
func (a *adaptiveStream) observe(settings viewerSettings, now time.Time, elapsed time.Duration, bytes int, loss float64) {
	if !settings.StreamAdaptive {
		a.fps = settings.StreamFPS
		a.quality = settings.StreamQuality
		a.bitrate = settings.StreamBitrate
		return
	}
	if now.Sub(a.last) < time.Second {
		return
	}
	a.last = now
	capacity := settings.StreamFPS
	if elapsed > 0 {
		capacity = min(capacity, max(1, int(float64(time.Second)*0.85/float64(elapsed))))
	}
	overBudget := float64(bytes)*float64(a.fps)*8 > float64(settings.StreamBitrate)*1000
	if capacity < a.fps || overBudget || loss > 0.02 {
		a.fps = max(1, min(capacity, max(1, a.fps*4/5)))
		a.quality = max(min(25, settings.StreamQuality), a.quality-8)
		a.bitrate = max(100, a.bitrate*4/5)
		a.healthy = 0
	} else {
		a.healthy++
		if a.healthy >= 3 {
			a.fps = min(settings.StreamFPS, a.fps+2)
			a.quality = min(settings.StreamQuality, a.quality+4)
			a.bitrate = min(settings.StreamBitrate, a.bitrate+max(100, settings.StreamBitrate/10))
			a.healthy = 0
		}
	}
}

type streamMeter struct {
	since         time.Time
	bytes, frames int
	dropped       uint64
}

func (v *Viewer) measureStream(meter *streamMeter, transport, encoding string, a adaptiveStream, bytes int, elapsed, encode time.Duration, skipped uint64) {
	now := time.Now()
	if meter.since.IsZero() {
		meter.since = now
	}
	meter.bytes += bytes
	meter.frames++
	meter.dropped += skipped
	seconds := now.Sub(meter.since).Seconds()
	if seconds < 1 {
		return
	}
	stats := &streamStats{Transport: transport, Encoding: encoding, FPS: float64(meter.frames) / seconds, TargetFPS: a.fps, Quality: a.quality, Bitrate: int(float64(meter.bytes) * 8 / seconds / 1000), RTT: float64(elapsed) / float64(time.Millisecond), EncodeMS: float64(encode) / float64(time.Millisecond), Dropped: meter.dropped}
	v.mu.Lock()
	v.state.Stream = stats
	v.mu.Unlock()
	meter.since = now
	meter.bytes = 0
	meter.frames = 0
}

func sameStreamSettings(a, b viewerSettings) bool {
	return a.StreamTransport == b.StreamTransport && a.StreamEncoding == b.StreamEncoding && a.StreamFPS == b.StreamFPS && a.StreamQuality == b.StreamQuality && a.StreamAdaptive == b.StreamAdaptive && a.StreamBitrate == b.StreamBitrate && a.StreamDirty == b.StreamDirty
}
