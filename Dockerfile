FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.buildDate=$BUILD_DATE" -o /out/clock-relay ./cmd/clock-relay

FROM alpine:3.22
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
LABEL org.opencontainers.image.title="Clock Relay" \
      org.opencontainers.image.description="Self-hosted scheduler and trigger layer for infrastructure jobs." \
      org.opencontainers.image.source="https://github.com/johnnycon/clock-relay" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$COMMIT \
      org.opencontainers.image.created=$BUILD_DATE
RUN adduser -D -H relay
WORKDIR /app
COPY --from=build /out/clock-relay /usr/local/bin/clock-relay
COPY clock-relay.example.yaml /app/clock-relay.yaml
RUN mkdir -p /app/data && chown -R relay:relay /app
USER relay
EXPOSE 9808
CMD ["clock-relay", "--config", "/app/clock-relay.yaml"]
