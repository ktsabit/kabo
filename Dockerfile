FROM node:24-alpine AS client-build
WORKDIR /src/client
COPY client/package.json client/package-lock.json ./
RUN npm ci
COPY client ./
COPY shared ../shared
COPY .git/HEAD /src/.git/HEAD
COPY .git/refs/heads /src/.git/refs/heads
ARG VITE_DISCORD_CLIENT_ID
ARG VITE_BUILD_VERSION
ENV VITE_DISCORD_CLIENT_ID=$VITE_DISCORD_CLIENT_ID
RUN set -eu; \
    version="$VITE_BUILD_VERSION"; \
    if [ -z "$version" ]; then \
      head_value="$(cat /src/.git/HEAD)"; \
      case "$head_value" in \
        "ref: "*) ref_path="${head_value#ref: }"; revision="$(cat "/src/.git/$ref_path")" ;; \
        *) revision="$head_value" ;; \
      esac; \
      version="$(printf '%.5s' "$revision")"; \
    fi; \
    test "${#version}" -eq 5; \
    VITE_BUILD_VERSION="$version" npm run build

FROM golang:1.24-alpine AS server-build
WORKDIR /src/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /cambio-server .

FROM alpine:3.22
RUN addgroup -S cambio && adduser -S cambio -G cambio
RUN mkdir -p /data && chown cambio:cambio /data
WORKDIR /app
COPY --from=server-build /cambio-server ./cambio-server
COPY --from=client-build /src/client/dist ./client/dist
USER cambio
ENV PORT=8080 CLIENT_DIST=/app/client/dist ALLOW_GUESTS=false DB_PATH=/data/kabo.sqlite KABO_TURN_TIMEOUT=15s KABO_REVEAL_TIMEOUT=3s
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 CMD wget -q -O - http://127.0.0.1:8080/healthz || exit 1
CMD ["./cambio-server"]
