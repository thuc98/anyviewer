package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/webrtc/v4"
)

func (v *Viewer) notifyStatus() {
	select {
	case v.statusWake <- struct{}{}:
	default:
	}
}
func (v *Viewer) startControl() (context.Context, context.CancelFunc, chan struct{}, uint64) {
	v.lifecycle.Lock()
	defer v.lifecycle.Unlock()
	v.mu.Lock()
	cancelOld, doneOld := v.controlCancel, v.controlDone
	v.mu.Unlock()
	if cancelOld != nil {
		cancelOld()
	}
	if doneOld != nil {
		<-doneOld
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	if v.closing {
		cancel()
	}
	v.mu.Lock()
	v.controlCancel, v.controlDone = cancel, done
	v.mu.Unlock()
	return ctx, cancel, done, v.controlEpoch.Add(1)
}
func (v *Viewer) finishControl(done chan struct{}, epoch uint64) {
	v.controlEpoch.CompareAndSwap(epoch, epoch+1)
	select {
	case v.releaseInputs <- struct{}{}:
	default:
	}
	close(done)
}

type controlInput struct {
	Type  string                 `json:"type"`
	ID    uint64                 `json:"id"`
	Event map[string]interface{} `json:"event"`
}

func (v *Viewer) controlMessage(data []byte, send func([]byte) error, epoch uint64) error {
	if epoch != 0 && epoch != v.controlEpoch.Load() {
		return errors.New("control channel closed")
	}
	var message controlInput
	if len(data) > 4096 || json.Unmarshal(data, &message) != nil {
		return errors.New("invalid control message")
	}
	switch message.Type {
	case "heartbeat":
		if v.onHeartbeat != nil && !v.onHeartbeat() {
			return errors.New("control session expired")
		}
	case "status_request":
		v.notifyStatus()
	case "input":
		err := v.eventWithEpoch(message.Event, epoch)
		response := map[string]interface{}{"type": "input_ack", "id": message.ID}
		if err != nil {
			response["error"] = err.Error()
		}
		packet, _ := json.Marshal(response)
		return send(packet)
	default:
		return errors.New("unknown control message")
	}
	return nil
}
func (v *Viewer) pumpStatus(ctx context.Context, send func([]byte) error) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var previous []byte
	for {
		v.mu.Lock()
		comparison := v.state
		if comparison.Frames > 0 {
			comparison.Frames = 1
		}
		changed, _ := json.Marshal(comparison)
		packet, _ := json.Marshal(map[string]interface{}{"type": "status", "status": v.state})
		v.mu.Unlock()
		if !bytes.Equal(changed, previous) {
			if err := send(packet); err != nil {
				return err
			}
			previous = changed
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case <-v.statusWake:
		}
	}
}
func (v *Viewer) serveControlSocket(w http.ResponseWriter, r *http.Request) {
	id := streamID(r)
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"viewer"}})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4096)
	ctx, cancel, done, epoch := v.startControl()
	defer cancel()
	defer v.finishControl(done, epoch)
	var writer sync.Mutex
	send := func(data []byte) error {
		writer.Lock()
		defer writer.Unlock()
		timeout, stop := context.WithTimeout(ctx, 3*time.Second)
		defer stop()
		return conn.Write(timeout, websocket.MessageText, data)
	}
	go func() {
		defer cancel()
		for {
			kind, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if kind != websocket.MessageText || v.controlMessage(data, send, epoch) != nil {
				return
			}
		}
	}()
	streamLogf(id, "event=stream_control status=connected transport=websocket")
	err = v.pumpStatus(ctx, send)
	streamLogf(id, "event=stream_control status=stopped transport=websocket error=%q", err)
}
func (v *Viewer) attachControlChannel(parent context.Context, channel *webrtc.DataChannel, id string) {
	var cancel context.CancelFunc
	var controlEpoch uint64
	ready := make(chan struct{})
	var cancelMu sync.Mutex
	var writer sync.Mutex
	send := func(data []byte) error {
		writer.Lock()
		defer writer.Unlock()
		if channel.ReadyState() != webrtc.DataChannelStateOpen {
			return errors.New("control channel closed")
		}
		if channel.BufferedAmount() > 64<<10 {
			return errors.New("control channel congested")
		}
		return channel.SendText(string(data))
	}
	channel.OnOpen(func() {
		ctx, stop, done, epoch := v.startControl()
		cancelMu.Lock()
		cancel = stop
		controlEpoch = epoch
		cancelMu.Unlock()
		close(ready)
		stopParent := context.AfterFunc(parent, stop)
		streamLogf(id, "event=stream_control status=connected transport=webrtc")
		go func() {
			defer stop()
			defer stopParent()
			defer v.finishControl(done, epoch)
			err := v.pumpStatus(ctx, send)
			channel.Close()
			streamLogf(id, "event=stream_control status=stopped transport=webrtc error=%q", err)
		}()
	})
	channel.OnMessage(func(message webrtc.DataChannelMessage) {
		select {
		case <-ready:
		case <-parent.Done():
			return
		}
		cancelMu.Lock()
		epoch := controlEpoch
		cancelMu.Unlock()
		if !message.IsString || epoch == 0 || v.controlMessage(message.Data, send, epoch) != nil {
			channel.Close()
		}
	})
	channel.OnClose(func() {
		cancelMu.Lock()
		stop := cancel
		cancelMu.Unlock()
		if stop != nil {
			stop()
		}
	})
}
