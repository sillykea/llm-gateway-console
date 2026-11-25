# 1. Build the web app.
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run typecheck && npm run build

# 2. Build the Go binary with the web app embedded.
FROM golang:1.26-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
COPY web/embed.go ./web/
COPY --from=web /web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /console ./cmd/console
# The data directory must exist and belong to the runtime user, so a volume
# mounted there is writable (distroless has no shell to create it later).
RUN mkdir -p /out/data

# 3. Run as a non-root user on a distroless base: no shell, no package manager.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /console /console
COPY --from=build --chown=65532:65532 /out/data /data
ENV CONSOLE_DATA_DIR=/data CONSOLE_USERS_FILE=/etc/console/users.json
EXPOSE 8090 8091
USER nonroot
ENTRYPOINT ["/console"]
