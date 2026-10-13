package controller

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/ivfreader"
)

type encodedVideo struct {
	data []byte
	err  error
}
type vp8Encoder struct {
	cmd     *exec.Cmd
	input   io.WriteCloser
	output  chan encodedVideo
	cancel  context.CancelFunc
	stderr  encoderStderr
	waitErr error
	once    sync.Once
}

type encoderStderr struct {
	mu   sync.Mutex
	data []byte
}

func (s *encoderStderr) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	const limit = 8192
	if len(data) >= limit {
		s.data = append(s.data[:0], data[len(data)-limit:]...)
	} else {
		if len(s.data)+len(data) > limit {
			s.data = s.data[len(s.data)+len(data)-limit:]
		}
		s.data = append(s.data, data...)
	}
	return len(data), nil
}
func (s *encoderStderr) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(string(s.data))
}

func newVP8Encoder(ctx context.Context, width, height, fps, bitrate int) (*vp8Encoder, error) {
	ctx, cancel := context.WithCancel(ctx)
	encoder := &vp8Encoder{cancel: cancel, output: make(chan encodedVideo, 1)}
	args := []string{"-hide_banner", "-loglevel", "error", "-probesize", "32", "-analyzeduration", "0", "-f", "rawvideo", "-pixel_format", "rgba", "-video_size", fmt.Sprintf("%dx%d", width, height), "-framerate", strconv.Itoa(fps), "-i", "pipe:0", "-an", "-c:v", "libvpx", "-deadline", "realtime", "-cpu-used", "8", "-lag-in-frames", "0", "-auto-alt-ref", "0", "-threads", "2", "-pix_fmt", "yuv420p", "-b:v", strconv.Itoa(bitrate) + "k", "-maxrate", strconv.Itoa(bitrate) + "k", "-bufsize", strconv.Itoa(bitrate) + "k", "-g", strconv.Itoa(fps), "-flush_packets", "1", "-f", "ivf", "pipe:1"}
	encoder.cmd = exec.CommandContext(ctx, "ffmpeg", args...)
	encoder.cmd.Stderr = &encoder.stderr
	input, err := encoder.cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	output, err := encoder.cmd.StdoutPipe()
	if err != nil {
		input.Close()
		cancel()
		return nil, err
	}
	encoder.input = input
	if err = encoder.cmd.Start(); err != nil {
		input.Close()
		output.Close()
		cancel()
		return nil, err
	}
	go func() {
		reader, _, err := ivfreader.NewWith(output)
		for {
			var data []byte
			if err == nil {
				data, _, err = reader.ParseNextFrame()
			}
			select {
			case encoder.output <- encodedVideo{data, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return encoder, nil
}
func (e *vp8Encoder) close() {
	e.once.Do(func() { e.cancel(); e.input.Close(); e.waitErr = e.cmd.Wait() })
}
func (e *vp8Encoder) encode(ctx context.Context, pixels *image.RGBA) ([]byte, error) {
	written := make(chan error, 1)
	go func() { _, err := e.input.Write(pixels.Pix); written <- err }()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case err := <-written:
		if err != nil {
			return nil, err
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, errors.New("video encoder input timed out")
	}
	select {
	case frame := <-e.output:
		return frame.data, frame.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, errors.New("video encoder output timed out")
	}
}

type videoFeedback struct {
	Type   string  `json:"type"`
	RTT    float64 `json:"rtt_ms"`
	Loss   float64 `json:"loss"`
	Jitter float64 `json:"jitter_ms"`
}

func (v *Viewer) pumpVideo(ctx context.Context, track *webrtc.TrackLocalStaticSample, feedback <-chan videoFeedback, keyframes <-chan struct{}, id string) (runErr error) {
	var encoder *vp8Encoder
	stage := "wait_for_source"
	frames := 0
	streamStarted := time.Now()
	streamLogf(id, "event=stream_video status=starting encoding=vp8")
	defer func() {
		if encoder != nil {
			encoder.close()
		}
		status := "stopped"
		if runErr != nil && ctx.Err() == nil {
			status = "failed"
		}
		streamLogf(id, "event=stream_video status=%s stage=%s frames=%d elapsed=%s error=%q", status, stage, frames, time.Since(streamStarted), runErr)
		if status == "failed" && encoder != nil {
			streamLogf(id, "event=stream_encoder status=failed exit_error=%q stderr=%q", encoder.waitErr, encoder.stderr.String())
		}
		if runErr != nil && ctx.Err() == nil {
			v.mu.Lock()
			v.state.Stream = &streamStats{Transport: "webrtc", Encoding: "vp8", Error: runErr.Error()}
			v.mu.Unlock()
		}
	}()
	ticker := time.NewTicker(time.Second / 60)
	defer ticker.Stop()
	var lastSettings viewerSettings
	var adaptive adaptiveStream
	var meter streamMeter
	var version uint64
	var size image.Point
	var next, encodedAt, lastSent time.Time
	var configuredFPS, configuredBitrate int
	var warmup int
	var network videoFeedback
	force := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case value := <-feedback:
			network = value
		case <-keyframes:
			force = true
		case now := <-ticker.C:
			if now.Before(next) {
				continue
			}
			v.mu.Lock()
			settings := v.settings
			currentVersion := v.screenVersion
			v.mu.Unlock()
			changed := !sameStreamSettings(settings, lastSettings)
			if changed {
				adaptive = newAdaptiveStream(settings)
			}
			if currentVersion == version && !force && !changed && now.Sub(lastSent) < time.Second {
				continue
			}
			snapshot := v.snapshotScreen(0, true)
			if snapshot.pixels == nil {
				if encoder != nil {
					encoder.close()
					encoder = nil
				}
				version = snapshot.version
				continue
			}
			dimensions := image.Pt(snapshot.width, snapshot.height)
			adjust := now.Sub(encodedAt) >= 5*time.Second && (configuredFPS != adaptive.fps || configuredBitrate != adaptive.bitrate)
			if encoder == nil || force || changed || size != dimensions || adjust {
				if encoder != nil {
					encoder.close()
				}
				var err error
				stage = "start_encoder"
				streamLogf(id, "event=stream_encoder status=starting width=%d height=%d fps=%d bitrate_kbps=%d keyframe_requested=%t", snapshot.width, snapshot.height, adaptive.fps, adaptive.bitrate, force)
				encoder, err = newVP8Encoder(ctx, snapshot.width, snapshot.height, adaptive.fps, adaptive.bitrate)
				if err != nil {
					return fmt.Errorf("VP8 encoder: %w", err)
				}
				configuredFPS, configuredBitrate = adaptive.fps, adaptive.bitrate
				warmup = 3
				size, encodedAt, force = dimensions, now, false
			}
			started := time.Now()
			stage = "encode_frame"
			data, err := encoder.encode(ctx, snapshot.pixels)
			if err != nil {
				return fmt.Errorf("VP8 encoding failed: %w", err)
			}
			elapsed := time.Since(started)
			duration := time.Second / time.Duration(adaptive.fps)
			if !lastSent.IsZero() {
				duration = started.Sub(lastSent)
			}
			stage = "send_sample"
			if err = track.WriteSample(media.Sample{Data: data, Duration: duration}); err != nil {
				return err
			}
			frames++
			if frames == 1 {
				streamLogf(id, "event=stream_video status=first_sample bytes=%d elapsed=%s encode_time=%s", len(data), time.Since(streamStarted), elapsed)
			}
			loss := network.Loss
			if network.Jitter > 100 {
				loss = max(loss, 0.03)
			}
			if warmup > 0 {
				warmup--
			} else {
				adaptive.observe(settings, time.Now(), elapsed, len(data), loss)
			}
			skipped := uint64(0)
			if version > 0 && snapshot.version > version+1 {
				skipped = snapshot.version - version - 1
			}
			v.measureStream(&meter, "webrtc", "vp8", adaptive, len(data), time.Duration(network.RTT*float64(time.Millisecond)), elapsed, skipped)
			version, lastSettings, lastSent = snapshot.version, settings, started
			next = started.Add(time.Second / time.Duration(adaptive.fps))
		}
	}
}
