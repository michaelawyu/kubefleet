# syntax=docker/dockerfile:1
# Build the mirrorer binary.
#
# Unlike the hub-agent/member-agent images, this binary needs no cgo (it uses no
# GOEXPERIMENT=systemcrypto or other cgo-dependent feature), so the build below is a plain,
# statically linked Go build for the build host's own platform; no C toolchain, and no
# multi-platform cross-compilation, is needed here.
#
# Build this image from the repository root (it needs go.mod/go.sum and the apis/ package
# alongside this directory), e.g.:
#   docker build -f hack/multitenancydemo/mirrorer/Dockerfile -t mirrorer .
FROM mcr.microsoft.com/oss/go/microsoft/golang:1.26.8-1 AS builder

# This Microsoft Go build defaults to GOEXPERIMENT=systemcrypto, which requires CGO_ENABLED=1;
# this binary needs no cgo, so the experiment is explicitly disabled below to allow a plain,
# statically linked build.
ENV GOEXPERIMENT=none

WORKDIR /workspace
# Copy the Go Modules manifests.
COPY go.mod go.mod
COPY go.sum go.sum
# Cache deps before building and copying source, so that source changes don't invalidate the
# downloaded-modules layer.
RUN go mod download

# Copy the Go source.
COPY apis/ apis/
COPY hack/multitenancydemo/mirrorer/ hack/multitenancydemo/mirrorer/

# CGO_ENABLED=0 keeps this a statically linked binary, so no C toolchain is needed here.
RUN CGO_ENABLED=0 go build -o mirrorer ./hack/multitenancydemo/mirrorer/cmd/

# Use distroless as minimal base image to package the mirrorer binary.
# Refer to https://github.com/GoogleContainerTools/distroless for more details.
FROM gcr.io/distroless/base:nonroot@sha256:0896741ba5bafd3ac87ea025a5f578952f2d238ddc3614cb368acc983a687aa2
WORKDIR /
COPY --link --from=builder /workspace/mirrorer .
USER 65532:65532

ENTRYPOINT ["/mirrorer"]
