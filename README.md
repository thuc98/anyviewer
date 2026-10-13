# AnyViewer Web

A browser-based remote desktop viewer with a Go backend and React frontend. The backend connects to an U********wer remote computer, reconstructs its screen updates, and streams the screen to the browser. The interface is in Vietnamese.

## Features

- Connect using a partner ID and password; control the mouse and keyboard or enable view-only mode.
- WebSocket and WebRTC streaming, with PNG/JPEG images and a VP8 video option for WebRTC.
- Dirty-region updates, adaptive quality/FPS, and a configurable bandwidth target.
- Persistent status, input, and heartbeat channels, with HTTP fallback.
- Text messaging, file transfer, and text clipboard synchronization.
- Light/dark themes, fit-to-window/fullscreen viewing, and a draggable translucent communication panel.
- Restore a tab's connected session after reload. Sessions expire after one minute without browser heartbeats.
- Stream diagnostics for negotiation, ICE, FFmpeg errors, fallback, and disconnect cleanup.

## Quick start with Docker

Docker Compose builds the frontend and backend and installs FFmpeg/libvpx, CA certificates, timezone data, and the health-check dependencies.

```sh
docker compose up -d --build
docker compose logs -f viewer
```

Open **http://localhost:8765**.

If another process uses port 8765:

```sh
VIEWER_PORT=18765 docker compose up -d --build
```

Open **http://localhost:18765**. Keep that port setting for later Compose operations, or copy `.env.example` to `.env` and set `VIEWER_PORT` there.

The service runs as an unprivileged user. Settings persist in the `viewer-data` volume at `/data/AnyViewer/settings.json`. Compose publishes the WebRTC UDP range **50000–50020** in addition to the HTTP port.

See [DOCKER.md](DOCKER.md) for LAN access, advertised WebRTC addresses, volume behavior, and dependency checks.

## Run from source

Requirements:

- Go 1.23 or newer; see `backend/go.mod` for the toolchain declaration.
- Node.js 22 and npm.
- FFmpeg with the `libvpx` encoder for VP8. PNG/JPEG streaming does not require FFmpeg.

Build and run from the repository root:

```sh
make build
./bin/anyviewer -open
```

The frontend is embedded in the Go binary. To rebuild and run directly:

```sh
make run
```

Use another port if needed:

```sh
make run ARGS="-port 18765 -open"
```

### Frontend development

First build the embedded frontend and start the backend with `make run`. In a second terminal:

```sh
cd frontend
npm run dev
```

Open the URL reported by Vite. Its proxy forwards API and WebSocket requests to the backend on port 8765. Rebuild and restart the backend after changing Go code.

## Connecting and stream settings

Enter the remote computer's **ID đối tác** and **Mật khẩu**, then select **Kết nối**. **Ngắt kết nối** waits for the backend controller to close its registration and relay sockets.

Settings are split into **Chung**, **Hình ảnh**, and **Mạng** tabs. In **Cài đặt → Hình ảnh**:

| Setting | Behavior |
| --- | --- |
| Cơ chế truyền hình ảnh | WebSocket, WebRTC, or HTTP polling |
| Định dạng hình ảnh | JPEG, PNG, or VP8 when WebRTC is selected |
| Chất lượng JPEG | Browser stream quality, from 1 to 100 |
| Tốc độ tối đa | Delivery cap, from 1 to 60 frames/second |
| Băng thông mục tiêu | Adaptive bandwidth target in kbps; default 10,000 kbps (10 Mbps) |
| Tự điều chỉnh luồng hình ảnh | Adjust FPS, JPEG quality, and VP8 bitrate to delivery conditions |
| Chỉ truyền vùng thay đổi | Send changed image regions; applies to PNG/JPEG |
| Chu kỳ cập nhật dự phòng | Refresh interval for HTTP polling |

Defaults are **WebSocket**, **JPEG quality 75**, **30 FPS maximum**, a **10 Mbps target**, and adaptive streaming/dirty regions enabled. The remote quality preset controls the images received from the partner; JPEG stream quality controls additional encoding between this backend and the browser.

To use VP8, select **WebRTC → VP8 · video thời gian thực**. FFmpeg must be available on the backend's PATH; it is included in the Docker image. The achieved frame rate depends on the remote source, encoding speed, and network.

If WebRTC fails, the browser switches to WebSocket. If WebSocket fails, it uses HTTP polling and retries the selected push mode after five seconds. VP8 falls back to JPEG over WebSocket. An idle viewer waiting for a remote screen does not trigger the video-frame timeout.

See [STREAMING.md](STREAMING.md) for the frame pipeline, adaptation, and diagnostics.

## Sessions and settings

The browser stores the session identifier for the current tab in `sessionStorage`. Reloading restores the same controller and partner ID without storing the remote password there. Heartbeats keep the session alive; after one minute without them, the backend shuts it down. Returning after expiry creates a disconnected session.

A lost control channel releases held keys/buttons and invalidates its queued input. Mouse movement retains only the latest pending position when congested; key and button events remain ordered.

Native settings use the operating system's user config directory under `AnyViewer/settings.json`. On macOS, this is normally `~/Library/Application Support/AnyViewer/settings.json`. Docker uses the persistent `/data` volume. Network/proxy settings apply to the next remote connection; stream settings apply immediately.

## Runtime options

```sh
./bin/anyviewer -help
```

| Flag | Default | Purpose |
| --- | --- | --- |
| `-host` | `127.0.0.1` | HTTP listen address |
| `-port` | `8765` | HTTP port; `0` selects a free port |
| `-open` | `false` | Open the browser on startup |
| `-width`, `-height` | `1024`, `768` | Initial screen dimensions |
| `-block-size` | `32` | JPEG atlas block size |
| `-registration` | generated | Optional controller configuration file |
| `-dictionary` | embedded | Optional Zstandard dictionary file |

Environment variables used for Docker/network deployment:

| Variable | Purpose |
| --- | --- |
| `VIEWER_PORT` | Compose's published HTTP port |
| `VIEWER_BIND` | Compose's host bind address; defaults to loopback |
| `VIEWER_ALLOWED_HOSTS` | Exact permitted HTTP hostnames/IPs including ports, separated by commas |
| `VIEWER_WEBRTC_IP` | IP advertised in WebRTC host candidates |
| `VIEWER_WEBRTC_UDP_MIN`, `VIEWER_WEBRTC_UDP_MAX` | Backend ICE UDP port range; Compose uses 50000–50020 |
| `XDG_CONFIG_HOME` | Settings directory on Linux; `/data` in Docker |

WebRTC uses direct host ICE candidates. STUN/TURN is not configured. For Docker/LAN access, the advertised address and published UDP ports must be reachable from the browser.

## Troubleshooting

- **VP8 falls back to JPEG:** search logs for the same `stream=<id>`. Check `status=failed` and `status=fallback`, their stage/reason, ICE state, and FFmpeg stderr. Browser console messages use the same ID. SDP payloads, passwords, and authentication tokens are not logged.
- **FFmpeg dependency:** `ffmpeg -hide_banner -encoders` must list `libvpx`. In Docker, run `docker compose exec viewer ffmpeg -hide_banner -encoders`.
- **Docker returns HTTP 403:** check `VIEWER_ALLOWED_HOSTS` against the browser URL, including its port.
- **WebRTC cannot connect through Docker:** check `VIEWER_WEBRTC_IP` and UDP 50000–50020. WebSocket remains available as fallback.
- **Clipboard does not work:** enable synchronization and grant the browser's clipboard permission. Browser clipboard APIs require localhost or HTTPS.
- **Remote still appears connected:** inspect `event=viewer_disconnect` and the native `sockets=closed` log. Confirm that you are viewing the intended tab/session.
- **Unsupported screen operation:** the decoder supports JPEG atlases, RGB555/Zstandard updates, cached tiles, and scroll copies. BitBlt updates are not supported.

Native logs go to standard output/error. Docker logs are available with:

```sh
docker compose logs -f viewer
```

## Verification

Build the embedded UI before running backend tests:

```sh
cd frontend
npm ci
npm test
npm run build
cd ../backend
go test -race ./...
```

For Docker:

```sh
docker compose config --quiet
docker compose up -d --build --wait
docker compose ps
```

## Repository layout

```text
backend/cmd/anyviewer/       Application entry point
backend/internal/controller/ Remote protocol, sessions, streaming, and input
backend/internal/webui/     Embedded frontend assets
frontend/src/              React UI and browser transports
Dockerfile                 Frontend/backend build and FFmpeg runtime image
compose.yaml               Ports, settings volume, health check, and logging
DOCKER.md                  Docker and LAN deployment details
STREAMING.md               Stream pipeline and tuning details
```
