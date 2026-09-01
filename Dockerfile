FROM node:24-alpine AS client-build
WORKDIR /src/client
COPY client/package.json client/package-lock.json ./
RUN npm ci
COPY client ./
COPY shared ../shared
ARG VITE_DISCORD_CLIENT_ID
ENV VITE_DISCORD_CLIENT_ID=$VITE_DISCORD_CLIENT_ID
RUN npm run build

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
