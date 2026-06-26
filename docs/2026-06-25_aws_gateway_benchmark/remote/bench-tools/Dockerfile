# Builds the mock backend and load generator into one small static image.
# Both binaries live in the final image; the compose service / docker run
# command selects which one to execute.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY mock ./mock
COPY loadgen ./loadgen
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/mock ./mock \
 && CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/loadgen ./loadgen

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mock /mock
COPY --from=build /out/loadgen /loadgen
# No ENTRYPOINT: each invocation picks the binary as its command, e.g.
#   docker run img /mock          (compose `command: ["/mock"]`)
#   docker run img /loadgen -url …
CMD ["/mock"]
