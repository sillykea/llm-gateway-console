# Builds the demo's helper programs: the mock LLM and the traffic generator.
FROM golang:1.26-alpine AS build
ARG APP
WORKDIR /src
COPY go.mod ./
COPY demo ./demo
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /app ./demo/${APP} && mkdir -p /out/state

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /app /app
COPY --from=build --chown=65532:65532 /out/state /state
USER nonroot
ENTRYPOINT ["/app"]
