Docker deployment
=================

The image builds the React UI and Go backend and includes FFmpeg with libvpx for VP8, CA certificates, timezone data, and curl for the health check. It runs as an unprivileged user. No Node.js or Go installation is needed on the host.

```sh
docker compose up -d --build
docker compose logs -f viewer
```

Open http://localhost:8765. If the native backend already uses that port, use:

```sh
VIEWER_PORT=18765 docker compose up -d --build
```

Then open http://localhost:18765. The host allowlist adjusts to the published port automatically. Keep the same port environment when running subsequent Compose commands, or copy `.env.example` to `.env` and set `VIEWER_PORT` there.

In Cài đặt → Hình ảnh select WebRTC and VP8. FFmpeg runs inside the container; video and reliable control messages use the published UDP range 50000–50020. The Docker setting advertises 127.0.0.1 as the WebRTC candidate address for the local browser. Installing FFmpeg alone is insufficient unless these ports and the advertised address match the host mapping.

For access from another machine on the LAN, set these values in `.env`:

```dotenv
VIEWER_PORT=8765
VIEWER_BIND=0.0.0.0
VIEWER_WEBRTC_IP=192.168.1.10
VIEWER_ALLOWED_HOSTS=192.168.1.10:8765,localhost:8765,127.0.0.1:8765
```

Replace the example IP with the Docker host's LAN address. The browser must reach TCP 8765 and UDP 50000–50020 on that host. This configuration uses direct host ICE candidates; it does not include a TURN service.

Settings persist in the `viewer-data` volume at `/data/AnyViewer/settings.json`. Recreating or restarting the container keeps them. `docker compose down -v` removes that volume and its settings.

Verify dependencies:

```sh
docker compose exec viewer ffmpeg -hide_banner -encoders
docker compose ps
```

The encoder list must include `libvpx` (VP8). Logs include the shared stream ID, ICE/peer states, client fallback reason, FFmpeg errors, and disconnect socket-closure messages.
