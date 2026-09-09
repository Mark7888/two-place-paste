# TwoPlacePaste relay server.
#
# Build context is the repository root, because the Go workspace spans three
# modules and the server module resolves pkg/tppclient (the generated protobuf
# bindings) through it:
#
#   docker build -f deploy/Dockerfile .
#
# docker-compose.yml in this directory does exactly that.

FROM golang:1.26-alpine AS build
WORKDIR /src

# Dependency layer: the module files change far less often than the code, so a
# code edit does not re-download the module cache.
COPY go.work ./
COPY server/go.mod server/go.sum ./server/
COPY pkg/tppclient/go.mod pkg/tppclient/go.sum ./pkg/tppclient/
COPY desktop/go.mod ./desktop/
# GOWORK=off keeps this to the server module's own requirements; the two other
# workspace modules contribute no dependencies of their own.
RUN cd server && GOWORK=off go mod download

COPY . .
# CGO is off: the result is a static binary that runs on the scratch-like
# runtime below without a libc.
RUN CGO_ENABLED=0 go build -C server -trimpath -ldflags="-s -w" -o /out/tpp ./cmd/tpp

FROM alpine:3.21
# wget comes from busybox and is what the compose healthcheck calls; there is
# no other reason for a shell in this image.
RUN adduser -D -u 10001 -h /var/lib/tpp tpp \
    && mkdir -p /var/lib/tpp/blobs \
    && chown -R tpp:tpp /var/lib/tpp

COPY --from=build /out/tpp /usr/local/bin/tpp

# The container runs unprivileged. The blob volume is mounted at a path this
# user owns; nothing in the image is writable by it.
USER 10001:10001
WORKDIR /var/lib/tpp
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/tpp"]
CMD ["serve"]
