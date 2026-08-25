# Dejarik — one static binary, nothing else in the image.
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /out/dejarik ./cmd/dejarik

FROM scratch
LABEL org.opencontainers.image.source="https://github.com/tomblancdev/dejarik" \
      org.opencontainers.image.description="Dejarik — the arcade's panel: what can I play, wake it, and my paired devices" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/dejarik /dejarik
VOLUME ["/data"]
USER 65532:65532
EXPOSE 8080
ENV DEJARIK_CONFIG=/etc/dejarik/config.yaml DEJARIK_DATA_DIR=/data
ENTRYPOINT ["/dejarik"]
