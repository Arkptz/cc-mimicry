# syntax=docker/dockerfile:1
# Multi-stage CGO build: compiles cc-mimicry.so (CLIProxyAPI c-shared plugin)
# against bookworm glibc, then packages it in a distroless artifact carrier.
#
# Consumer line (pin a version tag — do NOT use :latest):
#   COPY --from=ghcr.io/arkptz/cc-mimicry:v0.2.0 /plugin/cc-mimicry.so /plugins/linux/amd64/cc-mimicry.so

ARG GO_VERSION=1.26
ARG CPA_VERSION=v7.2.157-plugin3
ARG CPA_COMMIT=0bed1c8b2f2b28c9ad2b1e70172bbd3dc042d5bd

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential git \
    && rm -rf /var/lib/apt/lists/*

# Clone CLIProxyAPI SDK at the pinned tag, then verify the commit SHA so a moved
# tag cannot silently swap the SDK baked into the published .so.
# If upstream is private: docker build --secret id=github_token,env=GITHUB_TOKEN ...
# and use https://${GITHUB_TOKEN}@github.com/... in the clone URL.
ARG CPA_VERSION
ARG CPA_COMMIT
RUN git clone --depth 1 --branch "${CPA_VERSION}" \
      https://github.com/Arkptz/CLIProxyAPI.git /CLIProxyAPI \
    && ACTUAL=$(git -C /CLIProxyAPI rev-parse HEAD) \
    && if [ "$ACTUAL" != "${CPA_COMMIT}" ]; then \
         printf 'FATAL: tag %s resolved to %s, expected %s\n' \
           "${CPA_VERSION}" "$ACTUAL" "${CPA_COMMIT}" >&2; \
         exit 1; \
       fi

COPY go.mod go.sum ./
RUN go mod edit -replace github.com/router-for-me/CLIProxyAPI/v7=/CLIProxyAPI
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .

ARG VERSION=0.1.0
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 GOOS=linux go build \
      -buildvcs=false \
      -buildmode=c-shared \
      -trimpath \
      -ldflags="-s -w -X 'main.pluginVersion=${VERSION}'" \
      -o /out/cc-mimicry.so . \
    && rm -f /out/cc-mimicry.h

# Artifact carrier — distroless, carrying only the compiled .so + OCI metadata.
# This image is NOT run as a service; it exists solely for COPY --from consumers.
FROM gcr.io/distroless/static-debian12:nonroot AS artifact
ARG CPA_VERSION
ARG CPA_COMMIT
ARG VERSION
LABEL org.opencontainers.image.title="cc-mimicry" \
      org.opencontainers.image.description="CLIProxyAPI cc-mimicry plugin (.so artifact)" \
      cc-mimicry.cpa-sdk-version="${CPA_VERSION}" \
      cc-mimicry.cpa-sdk-commit="${CPA_COMMIT}" \
      cc-mimicry.plugin-version="${VERSION}"
COPY --from=build /out/cc-mimicry.so /plugin/cc-mimicry.so
