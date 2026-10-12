# Combined production image: Go binary (public entrypoint, port 8080) that
# reverse-proxies non-API traffic to the Angular SSR node server it spawns.

# --- Stage 1: build the Angular SSR app ---
FROM node:22-alpine AS frontend-build
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# --- Stage 2: build the Go backend ---
FROM golang:1.25-alpine AS backend-build
WORKDIR /app/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -o /out/hypertube ./cmd/hypertube

# --- Stage 3: runtime ---
FROM node:22-alpine
RUN apk add --no-cache ffmpeg
WORKDIR /app
COPY --from=backend-build /out/hypertube /app/hypertube
COPY --from=frontend-build /app/frontend/dist/frontend /app/frontend
COPY backend/migrations /app/migrations

ENV PORT=8080 \
    SSR_PORT=4000 \
    SSR_UPSTREAM=http://127.0.0.1:4000 \
    SSR_COMMAND="node /app/frontend/server/server.mjs" \
    DATA_DIR=/data

RUN mkdir -p /data && chown -R node:node /data /app
USER node

EXPOSE 8080
CMD ["/app/hypertube"]
