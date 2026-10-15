# Browser screen sharing

Open the application, click **Chia sẻ**, then **Chọn màn hình và chia sẻ**.
Choose a screen, window, or browser tab. The host registers an U*********er ID and
an eight-digit random password. Enter those credentials in the native viewer.
Each sharing session supports one viewer. Once connected, use **Nhắn tin với
người xem** inside the sharing dialog to exchange text with that viewer. Enter
sends a message; Shift+Enter inserts a new line. Sharing chat is isolated from
the outgoing viewing session and does not grant input or clipboard access.

Browser sharing transmits images only. Remote keyboard, mouse, audio, clipboard,
and file commands are not applied to the browser or backend machine. The sharing
module includes capability-checked keyboard/mouse/clipboard adapter hooks for a
future native source; it does not install an OS input injector.

## Image path

The default is up to 30 FPS, JPEG quality 92%, and a 1920×1080 size limit.
The sharing dialog offers HD, Full HD, or 2560×1440, quality 80/92/98%, and
15/30 FPS. Resolution preserves the source aspect ratio and does not upscale.
The initial canvas size remains fixed; source-window resizes are letterboxed.

A background worker compares 32-pixel tiles and encodes only the bounding region
that changed. Unchanged captures and duplicate decoded video frames are skipped. Idle sources
send a lightweight heartbeat every five seconds instead of resending full images. JPEG is encoded in native
bottom-up orientation and forwarded without another JPEG encode. Browsers without
worker canvas support use the full-frame compatibility path.

Frames upload through the authenticated `/share/ws` WebSocket. While connected,
an upload is acknowledged only after that version has been written to the relay.
This permits at most one frame ahead of the frame awaiting the viewer
acknowledgement and prevents capture from outrunning a slow receiver. Before a
viewer connects, upload acknowledgements limit capture to five uploads per second. Slow receivers get merged pending regions; newly connected
receivers get the complete reconstructed screen. A bounded 32-region history
falls back to a full snapshot when the receiver falls further behind. Full native
JPEGs stay compressed; a pixel canvas is reconstructed only when a partial patch
needs its base. JPEG quality
is reduced only when needed to fit the relay's 1 MB packet limit. Actual FPS
still depends on capture, JPEG processing, relay round-trip time, and the receiver.

The image transport setting for **viewing** (WebRTC/WebSocket/polling, VP8/JPEG/PNG)
does not change the native sharing format.

## Identity and lifecycle

Sharing has a distinct machine identity from the outbound viewer and other tabs.
The registration connection remains open to receive invitations. Each invitation
supplies relay candidates; the host opens a relay socket, advertises its assigned
address, verifies the incoming password proofs, and sends screen geometry before
frames. Passwords and login payloads are not written to logs.

**Dừng chia sẻ**, closing the sharing dialog, losing the upload socket, or session
expiry stops capture and closes registration/relay sockets. Reloading stops
sharing: browsers require another user gesture to select a capture source.
A new sharing attempt generates new credentials.

The existing session token protects status and upload endpoints. A separate
`source_id` identifies the current capture generation so delayed stop/upload
requests cannot attach to or stop a newer source.

## Endpoints

| Endpoint | Purpose |
| --- | --- |
| `POST /share/start` | Start with `width`, `height`, optional `quality` (30–100) and `encoding` (`jpeg` or `native-jpeg`); returns `source_id` |
| `GET /share` | Authenticated sharing state, ID/password, dimensions, frame count |
| `GET /share/ws?source_id=...` | Authenticated binary image upload; text `ack` after acceptance |
| `POST /share/chat` | Send `message` to the authenticated viewer, with matching `source_id` |
| `POST /share/stop` | Stop the matching `source_id`; stale identifiers are ignored |

All endpoints use the existing viewer token/session and origin checks. WebSocket
credentials use the existing subprotocol scheme.

For `native-jpeg`, each binary upload starts with a four-byte big-endian JSON
header length, followed by that JSON and the bottom-up JPEG bytes. Header fields:
`width`, `height` (full canvas), `x`, `y` (patch position), `full`, and
`encoding: "native-jpeg"`. The JPEG supplies patch dimensions. The first frame
must cover the full canvas. Legacy `jpeg` uploads contain an ordinary full-frame
JPEG without a header. A text `ping` receives `ack` without changing the screen.

## Verification

```bash
cd frontend
npm test
npm run build
cd ../backend
go test -race ./...
UV_SHARING_INTEGRATION=1 go test -race ./internal/controller -run TestSharingLiveRelay -v -count=1
```

## Performance diagnostics

The backend logs `event=sharing_stream` while frames are flowing, with delivered
FPS, kbps, and average viewer acknowledgement time (`ack_ms`). These values help
separate capture/encoding work from relay and viewer latency. They contain no
screen content or credentials.

Run the Full HD upload benchmark with:

```bash
cd backend
go test ./internal/controller -run '^$' -bench BenchmarkSharingNativeUpload1080 -benchmem
```

This measures backend upload processing only; it does not predict end-to-end FPS.
