# Build stage — run on the build host's native arch for speed, cross-compile for target
FROM --platform=$BUILDPLATFORM golang:1.26-alpine3.23 AS builder

ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT

WORKDIR /app

# Install ca-certificates for HTTPS requests
RUN apk add --no-cache ca-certificates=20251003-r0

# Download dependencies first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source and cross-compile for the target platform
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOARM=${TARGETVARIANT#v} go build -ldflags="-s -w" -o /gomodel ./cmd/gomodel

# Create .cache and data directories for runtime (with placeholder for COPY)
RUN mkdir -p /app/.cache /app/data && touch /app/.cache/.keep /app/data/.keep

# Runtime stage
FROM gcr.io/distroless/static-debian12:nonroot

# Copy binary and ca-certificates
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /gomodel /gomodel
COPY --from=builder /app/config/*.yaml /app/config/

# Create writable .cache and data directories for nonroot user (UID=65532)
COPY --from=builder --chown=65532:65532 /app/.cache /app/.cache
COPY --from=builder --chown=65532:65532 /app/data /app/data

WORKDIR /app

EXPOSE 8080

ENTRYPOINT ["/gomodel"]
