# Runtime only. scripts/build_container.sh supplies a verified minimal context.
FROM docker.io/library/alpine:3.24.3@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a AS runtime-base

FROM runtime-base AS verified
ARG TARGETOS
ARG TARGETARCH
ARG EXPECTED_MANIFEST_SHA256
ARG EXPECTED_CHECKSUMS_SHA256
COPY artifacts/ /artifacts/
COPY scripts/verify_release_bundle.sh /verify_release_bundle.sh
RUN --network=none test "$TARGETOS" = linux \
    && artifact="$(sh /verify_release_bundle.sh /artifacts "$EXPECTED_MANIFEST_SHA256" "$EXPECTED_CHECKSUMS_SHA256" "$TARGETOS" "$TARGETARCH")" \
    && mkdir /verified \
    && cp "/artifacts/$artifact" /verified/reproq \
    && chmod 0755 /verified/reproq

FROM runtime-base
WORKDIR /app
COPY --from=verified /verified/reproq ./reproq
COPY --from=verified /artifacts/BUILD-MANIFEST.txt ./BUILD-MANIFEST.txt
COPY migrations/ ./migrations/
ARG SOURCE_REVISION
ARG WORKER_VERSION
ARG RELEASE_STATUS
LABEL org.opencontainers.image.source="https://github.com/adpena/reproq-worker" \
      org.opencontainers.image.revision="$SOURCE_REVISION" \
      org.opencontainers.image.version="$WORKER_VERSION" \
      io.reproq.release-status="$RELEASE_STATUS"
CMD ["./reproq", "worker"]
