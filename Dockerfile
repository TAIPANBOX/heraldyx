# The notifier as a published image.
#
# Until now every deployment BUILT this from source on the machine it was
# installing to, which cost fifteen to forty minutes on a first run and put the
# whole toolchain in the blast radius of an install. Two of the three defects a
# live run found on 2026-08-02 lived in that build path rather than in any
# service. What nobody builds, nobody breaks.
#
# Static, distroless, non-root, multi-arch. CGO off is what makes the binary
# runnable on distroless static AND what makes cross-compiling to arm64 free:
# there is no C toolchain to arrange, so the arm64 image costs the same as the
# amd64 one.
#
# NEEDS BUILDKIT. `$BUILDPLATFORM` is a BuildKit variable, so `docker build`
# with the legacy builder expands it to nothing and fails with
# "failed to parse platform : \"\" is an invalid OS component". BuildKit is the
# default in Docker 23+ and in Docker Desktop; a host without it needs
# `docker buildx build`, or drop the `--platform=` from the line below and lose
# only the cross-compile (arm64 then builds under emulation, which is roughly
# fifteen times slower).
#
# Measured 2026-08-03 on an Ubuntu node whose docker had no buildx.
ARG GO_VERSION=1.26

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ENV GOTOOLCHAIN=auto
WORKDIR /src
# Dependencies first, so a code-only change does not re-download the module
# graph on every build.
COPY go.mod go.su[m] ./
RUN go mod download
COPY . .
# TARGETARCH comes from buildx, one value per platform being built. Building
# FROM the build platform and cross-compiling, rather than emulating the target
# under QEMU, is the difference between a minute and a quarter of an hour.
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/heraldyx ./cmd/heraldyx

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="heraldyx"
LABEL org.opencontainers.image.description="Reads the agent stack's shared event log and mails the operator, with a link into their own console and never an action."
LABEL org.opencontainers.image.source="https://github.com/TAIPANBOX/heraldyx"
LABEL org.opencontainers.image.licenses="Apache-2.0"
# The event log and this process's own state are mounted, never baked.
VOLUME ["/var/lib/stack"]
COPY --from=build /out/heraldyx /usr/local/bin/service
# 65532 is distroless's `nonroot` uid. Numeric on purpose: a kubelet with
# runAsNonRoot cannot verify a NAME and refuses the container outright.
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/service"]
