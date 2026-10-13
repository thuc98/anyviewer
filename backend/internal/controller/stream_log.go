package controller

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
)

var streamSequence atomic.Uint64

func validStreamID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
func streamID(r *http.Request) string {
	id := r.URL.Query().Get("stream_id")
	if validStreamID(id) {
		return id
	}
	return "s" + strconv.FormatUint(streamSequence.Add(1), 10)
}
func streamLogf(id, format string, args ...any) {
	log.Printf("stream=%s %s", id, fmt.Sprintf(format, args...))
}

type streamDiagnostic struct {
	ID         string `json:"stream_id"`
	Event      string `json:"event"`
	Stage      string `json:"stage"`
	Reason     string `json:"reason"`
	From       string `json:"from"`
	To         string `json:"to"`
	Encoding   string `json:"encoding"`
	Connection string `json:"connection_state"`
	ICE        string `json:"ice_state"`
	Signaling  string `json:"signaling_state"`
}

func (v *Viewer) serveStreamLog(w http.ResponseWriter, r *http.Request) {
	var diagnostic streamDiagnostic
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&diagnostic); err != nil || !validStreamID(diagnostic.ID) || (diagnostic.Event != "fallback" && diagnostic.Event != "first_frame") {
		reply(w, 400, []byte(`{"error":"invalid stream diagnostic"}`), "application/json")
		return
	}
	clean := func(value string) string {
		value = strings.ReplaceAll(value, "\n", " ")
		value = strings.ReplaceAll(value, "\r", " ")
		if len(value) > 512 {
			value = value[:512]
		}
		return value
	}
	streamLogf(diagnostic.ID, "event=stream_client status=%s stage=%q from=%q to=%q encoding=%q connection_state=%q ice_state=%q signaling_state=%q reason=%q", diagnostic.Event, clean(diagnostic.Stage), clean(diagnostic.From), clean(diagnostic.To), clean(diagnostic.Encoding), clean(diagnostic.Connection), clean(diagnostic.ICE), clean(diagnostic.Signaling), clean(diagnostic.Reason))
	reply(w, 200, []byte(`{"ok":true}`), "application/json")
}
