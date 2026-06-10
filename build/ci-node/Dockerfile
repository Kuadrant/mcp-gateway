# syntax=docker/dockerfile:1-labs

# kindest/node with the e2e infra and test-server images pre-seeded into the
# containerd content store. built by `make ci-node-image-build` (which
# composes BAKED_IMAGE_REFS from the pinned make variables and manifests),
# published by .github/workflows/ci-node-image.yaml under the tag printed by
# utils/ci-node-image-hash.sh, and consumed by the e2e workflows on a tag hit.
# kubeadm init and the installs still run normally at cluster create time;
# only the image pulls become no-ops. on a tag miss e2e falls back to stock
# kindest/node and the standard pull path.

ARG KIND_NODE_IMAGE
FROM ${KIND_NODE_IMAGE}

# space-separated image refs to seed
ARG BAKED_IMAGE_REFS

# containerd is not running during build, so start it for the duration of the
# RUN step, pull in parallel with one retry each (mirroring the Makefile's
# pull-images-into-kind macro) and stop it cleanly so the image store the
# layer captures is consistent. layer extraction mounts tmpmounts under
# /var/lib/containerd, hence --security=insecure (granted by the buildx
# builder ci-node-image-build creates)
RUN --security=insecure \
    [ -n "${BAKED_IMAGE_REFS}" ] || { echo "BAKED_IMAGE_REFS not set"; exit 1; }; \
    containerd >/var/log/containerd-bake.log 2>&1 & \
    containerd_pid=$!; \
    for _ in $(seq 1 30); do \
        ctr --namespace k8s.io images ls >/dev/null 2>&1 && break; \
        sleep 1; \
    done; \
    ctr --namespace k8s.io images ls >/dev/null; \
    pids=""; \
    for ref in ${BAKED_IMAGE_REFS}; do \
        ( ctr --namespace k8s.io images pull "${ref}" >/dev/null \
            || { echo "Retrying pull of ${ref}..."; sleep 2; \
                ctr --namespace k8s.io images pull "${ref}" >/dev/null; } ) & \
        pids="${pids} $!"; \
    done; \
    rc=0; \
    for pid in ${pids}; do wait "${pid}" || rc=1; done; \
    [ "${rc}" -eq 0 ] || { echo "ERROR: failed to pull one or more images"; exit 1; }; \
    echo "Seeded images:"; \
    ctr --namespace k8s.io images ls -q; \
    kill "${containerd_pid}"; \
    wait "${containerd_pid}" || true
