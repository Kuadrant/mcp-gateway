# syntax=docker/dockerfile:1

# kindest/node with the e2e infra and test-server images pre-seeded into the
# containerd content store. built by `make ci-node-image-build` (which
# composes BAKED_IMAGE_REFS from the pinned make variables and manifests),
# published by .github/workflows/ci-node-image.yaml under the tag printed by
# utils/ci-node-image-hash.sh, and consumed by the e2e workflows on a tag hit.
# kubeadm init and the installs still run normally at cluster create time;
# only the network image pulls become no-ops. on a tag miss e2e falls back to
# stock kindest/node and the standard pull path.

ARG KIND_NODE_IMAGE
FROM ${KIND_NODE_IMAGE}

# space-separated image refs to seed
ARG BAKED_IMAGE_REFS
# platform to fetch (linux/amd64 in CI); provided automatically by buildkit
ARG TARGETPLATFORM

# containerd is not running during build, so start it for the duration of the
# RUN step, fetch each image's content in parallel with one retry each and stop
# it cleanly so the content store the layer captures is consistent.
#
# `content fetch` seeds the blobs and registers the image without unpacking it
# into snapshots. unpacking here is impossible: converting an image layer's
# aufs whiteouts to overlayfs whiteouts creates device nodes in the overlay
# snapshotter's upperdir, and that upperdir sits on buildkit's own overlay
# rootfs (overlay-on-overlay), where the kernel denies whiteout creation
# ("operation not permitted"). kind unpacks each image at runtime on first use,
# on its single-level overlay where whiteout creation works. skipping the
# build-time unpack also means this RUN needs no extra mounts, so no
# security.insecure entitlement.
RUN \
    [ -n "${BAKED_IMAGE_REFS}" ] || { echo "BAKED_IMAGE_REFS not set"; exit 1; }; \
    containerd >/var/log/containerd-bake.log 2>&1 & \
    containerd_pid=$!; \
    for _ in $(seq 1 30); do \
        ctr --namespace k8s.io images ls >/dev/null 2>&1 && break; \
        sleep 1; \
    done; \
    ctr --namespace k8s.io images ls >/dev/null 2>&1 \
        || { echo "ERROR: containerd failed to start"; cat /var/log/containerd-bake.log; exit 1; }; \
    pids=""; \
    for ref in ${BAKED_IMAGE_REFS}; do \
        ( ctr --namespace k8s.io content fetch --platform "${TARGETPLATFORM}" "${ref}" >/dev/null \
            || { echo "Retrying fetch of ${ref}..."; sleep 2; \
                ctr --namespace k8s.io content fetch --platform "${TARGETPLATFORM}" "${ref}" >/dev/null; } ) & \
        pids="${pids} $!"; \
    done; \
    rc=0; \
    for pid in ${pids}; do wait "${pid}" || rc=1; done; \
    [ "${rc}" -eq 0 ] || { echo "ERROR: failed to fetch one or more images"; exit 1; }; \
    echo "Seeded images:"; \
    ctr --namespace k8s.io images ls -q; \
    kill "${containerd_pid}"; \
    wait "${containerd_pid}" || true
