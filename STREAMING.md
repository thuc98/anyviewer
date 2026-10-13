Screen streaming
================

Open Cài đặt → Hình ảnh to select WebSocket, WebRTC (image DataChannel), or HTTP polling. Saving applies stream settings immediately; it does not reconnect the remote computer.

The default is WebSocket, JPEG quality 75, maximum 30 FPS, a 10000 kbps bandwidth target, adaptive streaming, and dirty regions enabled. The remote quality preset controls the images received from UltraViewer; stream quality separately controls JPEG images sent from this backend to the browser.

- JPEG reduces bandwidth; lower quality reduces image detail.
- PNG preserves the reconstructed image without additional loss. JPEG quality has no effect on PNG.
- Maximum FPS (1–60) caps delivery, but cannot increase the remote computer's update rate.
- Dirty regions come directly from decoded update rectangles; the backend does not scan the full screen to discover changes. Tiles are decoded and validated before in-place commit. Stream readers copy only the selected region under a screen lock. Dirty regions send the bounding rectangle of changed tiles. Reconnects, resizing, encoding changes, and periodic refreshes send a full image. A patch covering at least 75% of the screen is sent as a full image.
- PNG/JPEG push transports wait for a browser render acknowledgement and skip intermediate source frames when necessary. The next dirty patch includes changes from all skipped updates.
- HTTP polling retains the existing full-PNG behavior. Its interval is configured separately; push encoding, quality, FPS, and dirty-region controls do not apply to polling.

WebRTC supports reliable, ordered PNG/JPEG image chunks and a real VP8 RTP video track. Select WebRTC plus VP8 for scrolling and animation. VP8 requires `ffmpeg` with the `libvpx` encoder on the backend PATH. This machine already has that encoder. On other machines install FFmpeg (for example `brew install ffmpeg` on macOS or `apt install ffmpeg` on Debian/Ubuntu). If the encoder is missing or fails, the client falls back to JPEG over WebSocket. It uses host ICE candidates for the current local/LAN viewer deployment; no STUN/TURN service is configured. If WebRTC fails, the client switches to WebSocket. If WebSocket fails, the client uses polling and retries the selected push transport after five seconds. The toolbar shows the active transport, and a banner explains fallback.

Adaptive streaming treats FPS, JPEG quality, and bandwidth as upper targets. PNG/JPEG delivery responds to encode/send/render acknowledgement time and estimated bandwidth. VP8 responds to encoding time and browser feedback for packet loss and jitter; encoder FPS/bitrate changes are applied at most every five seconds. Disabling adaptation retains the chosen settings. The bitrate setting is a target, not a strict traffic limit.

VP8 sends samples without waiting for image acknowledgements. FFmpeg runs with realtime encoding and no lookahead. Browser rendering uses decoded video frame callbacks. Resolution changes and RTCP keyframe requests restart the encoder to send a fresh keyframe. Video source snapshots are still full frames because the video codec consumes the whole image; PNG/JPEG snapshots can be small dirty patches.
