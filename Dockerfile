FROM node:22-bookworm-slim AS frontend
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

FROM golang:1.26-bookworm AS backend
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
COPY --from=frontend /src/backend/internal/webui/dist ./internal/webui/dist
RUN CGO_ENABLED=0 go build -trimpath -o /out/anyviewer ./cmd/anyviewer

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ffmpeg ca-certificates curl tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --create-home viewer \
    && mkdir -p /data/AnyViewer \
    && chown -R viewer:viewer /data
COPY --from=backend /out/anyviewer /usr/local/bin/anyviewer
ENV XDG_CONFIG_HOME=/data TZ=Asia/Ho_Chi_Minh
USER viewer
EXPOSE 8765/tcp 50000-50020/udp
ENTRYPOINT ["anyviewer"]
CMD ["-host", "0.0.0.0", "-port", "8765"]
