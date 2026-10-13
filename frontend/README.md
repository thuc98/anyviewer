# AnyViewer frontend

React with JavaScript/JSX and Vite. No TypeScript.

```sh
npm install
npm run build
```

Build writes the production UI to `../golang/web/`, embedded by the Go server.
Run `go run ./` in `../golang/` and open http://127.0.0.1:8765.
Rebuild/restart Go after editing frontend source.

For development, start Go on port 8765 and run `npm run dev` here. Vite proxies
API requests, preserves the viewer token, and rewrites Origin to the Go origin.

Features: remote input/frame display, collapsible floating chat/file panel,
receiver-acknowledged file progress, persistent machine name and live JPEG quality,
fit/actual-size display, fullscreen, adaptive polling and view-only mode.
Browser storage remembers only Partner ID, never passwords. Machine name, quality, clipboard opt-in, theme, refresh interval, fit and view-only preferences are saved in `viewer_settings` inside `golang/controller-config.json` (or the supplied registration path).

The connection form and controls share one compact bar. After connection the
remote viewport uses the full window, while the controls slide out of view.
Move to the top edge (or activate its keyboard-accessible handle) to reveal them;
they slide away after leaving the controls. Fullscreen keeps these controls and
the floating chat/file panel available. No screen title or footer occupies space.

Text clipboard sync is opt-in in Settings. After granting browser permission it
synchronizes while connected and focused; the toolbar also has explicit Send and
Receive actions. Ctrl+V sends local text before the remote V keystroke; with sync
enabled, macOS Command modifiers are mapped to Windows Ctrl. Clipboard text never
persists to disk. Only nonempty text up to 64 KB UTF-16LE is supported; use Send
File for files. Browser restrictions can require manual Send/Receive, especially
on Safari/Firefox or non-HTTPS LAN origins.

Dark theme uses #0b0d13 as its base. Light/Dark can be switched from the floating
toolbar or Settings and survives server/browser restarts.

The chat/file panel uses a small arrow at the right edge, not a large labeled
button. Drag its slim top grip to move it; hide/show preserves position. Incoming
files appear in the File tab with Accept/Reject, receive progress, Cancel and
Download when complete. Downloads require the viewer token. Received temp files
are retained after disconnect, removed on reconnect or server exit.
