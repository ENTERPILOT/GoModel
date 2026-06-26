# Builds the mock backend and load generator into one small static image.
# Both binaries live in the final image; the compose service / docker run
# command selects which one to execute.
FROM golang:1.26-alpine@sha256:3ad57304ad93bbec8548a0437ad9e06a455660655d9af011d58b993f6f615648 AS build
WORKDIR /src
COPY go.mod ./
COPY mock ./mock
COPY loadgen ./loadgen
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/mock ./mock \
 && CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/loadgen ./loadgen

FROM gcr.io/distroless/static-debian12:nonroot@sha256:d093aa3e30dbadd3efe1310db061a14da60299baff8450a17fe0ccc514a16639
COPY --from=build /out/mock /mock
COPY --from=build /out/loadgen /loadgen
# No ENTRYPOINT: each invocation picks the binary as its command, e.g.
#   docker run img /mock          (compose `command: ["/mock"]`)
#   docker run img /loadgen -url …
CMD ["/mock"]
