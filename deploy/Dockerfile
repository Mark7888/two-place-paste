# TwoPlacePaste relay server.
#
# Build context is the repository root, because the Go workspace spans three
# modules and the server module resolves pkg/tppclient (the generated protobuf
# bindings) through it:
#
#   docker build -f deploy/Dockerfile .
#
# docker-compose.yml in this directory does exactly that.

# The build stage runs on the builder's own platform and cross-compiles, so a
# multi-platform build (docker.yml: linux/amd64 and linux/arm64) compiles Go
# natively instead of under emulation. A plain `docker build` sets both
# platforms to the host's and builds what it always did.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

# Node runs scripts/notices, which writes the third-party notices the image
# ships. It stays in this stage; the runtime image has no Node.
RUN apk add --no-cache nodejs

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
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -C server -trimpath -ldflags="-s -w" -o /out/tpp ./cmd/tpp

# The binary carries the code of every Go module it was built from, and their
# licenses ask that a binary distribution carry their notices too (LICENSING.md).
RUN node scripts/notices/notices.mjs \
      --title "TwoPlacePaste relay (tpp): third-party notices" \
      --intro "The relay is licensed under the GNU Affero General Public License v3.0 or later (LICENSE in this directory) and includes code from pkg/tppclient, licensed under the Mozilla Public License 2.0 (LICENSE.MPL-2.0). Its source code is at https://github.com/Mark7888/two-place-paste. It also includes the third-party software below." \
      --go server=./cmd/tpp --goos "$TARGETOS" \
      --out /out/THIRD_PARTY_NOTICES.txt

FROM alpine:3.21
# wget comes from busybox and is what the compose healthcheck calls; there is
# no other reason for a shell in this image.
RUN adduser -D -u 10001 -h /var/lib/tpp tpp \
    && mkdir -p /var/lib/tpp/blobs \
    && chown -R tpp:tpp /var/lib/tpp

COPY --from=build /out/tpp /usr/local/bin/tpp
# The relay's own licenses and the notices of everything it is built from.
COPY --from=build /src/server/LICENSE /usr/share/doc/tpp/LICENSE
COPY --from=build /src/LICENSE /usr/share/doc/tpp/LICENSE.MPL-2.0
COPY --from=build /out/THIRD_PARTY_NOTICES.txt /usr/share/doc/tpp/THIRD_PARTY_NOTICES.txt

# The container runs unprivileged. The blob volume is mounted at a path this
# user owns; nothing in the image is writable by it.
USER 10001:10001
WORKDIR /var/lib/tpp
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/tpp"]
CMD ["serve"]
