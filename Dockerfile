# Build the two managers, then package them in one image. The two controller
# Deployments run the same image with different commands (see the config/ trees).
FROM golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# NOTE: this self-contained build only works once the module pins vates-kube-os
# with a released tag. While go.mod carries a `replace ... => ../../OS/main`
# directive, use Dockerfile.dev (binaries built on the host) for local builds.
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY api/ api/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o bootstrap-manager ./cmd/bootstrap
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o controlplane-manager ./cmd/controlplane

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/bootstrap-manager /bootstrap-manager
COPY --from=builder /workspace/controlplane-manager /controlplane-manager
USER 65532:65532
