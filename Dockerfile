# Combined production image: Go binary (public entrypoint, port 8080) that
# reverse-proxies non-API traffic to the Angular SSR node server it spawns.

# --- Stage 1: build the Angular SSR app ---
FROM node:22-alpine AS frontend-build
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
# Public path prefix, e.g. /hypertube/ behind the 42 portal.
ARG BASE_HREF=/
RUN npm run build -- --base-href "$BASE_HREF"

# --- Stage 2: build the Go backend ---
FROM golang:1.25-alpine AS backend-build
WORKDIR /app/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -o /out/hypertube ./cmd/hypertube

# --- Stage 3: runtime ---
FROM node:22-alpine
ARG BASE_HREF=/
RUN apk add --no-cache ffmpeg
WORKDIR /app
COPY --from=backend-build /out/hypertube /app/hypertube
COPY --from=frontend-build /app/frontend/dist/frontend /app/frontend
COPY backend/migrations /app/migrations

ENV PORT=8080 \
    SSR_PORT=4000 \
    SSR_UPSTREAM=http://127.0.0.1:4000 \
    SSR_COMMAND="node /app/frontend/server/server.mjs" \
    DATA_DIR=/data \
    BASE_PATH=$BASE_HREF

RUN mkdir -p /data && chown -R node:node /data /app
USER node

# 8080 HTTP; 6881 BitTorrent peers (TORRENT_LISTEN_PORT), TCP+UDP.
EXPOSE 8080 6881/tcp 6881/udp
CMD ["/app/hypertube"]
