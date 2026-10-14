# Braid zone runtime — one image, two postures (GO_PRODUCTION_PLAN.md §3 Phase 3c).
#
# The same binary serves either zone; BRAID_ZONE (set per Container class in the
# Worker) selects which handlers mount. The security split is enforced by the
# container, not the image: the compute Container is started with internet
# disabled and is never handed an Artifacts token; the io Container holds the
# scoped tokens and has egress only to the Artifacts host.
#
# The runtime stage keeps the full Go toolchain because the compute zone builds
# and runs the agents' tests (Rungs 2 + 4). GOTOOLCHAIN=local pins it to the
# image's Go so a repo's `go` directive can never trigger a toolchain download —
# which the compute zone's default-deny egress would block anyway.

FROM golang:1.26.3-bookworm AS build
WORKDIR /src
# Stdlib-only module (no go.sum); copy go.mod first for layer caching.
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -o /out/braid ./cmd/braid

FROM golang:1.26.3-bookworm
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/braid /usr/local/bin/braid

# Go caches must be writable and off-network in the compute zone.
ENV GOTOOLCHAIN=local \
    GOPATH=/tmp/go \
    GOCACHE=/tmp/gocache \
    GIT_TERMINAL_PROMPT=0
# gitx inherits the process env, so these give the merge engine's commits an
# identity without a global git config (gitio scrubs them; it never commits).
ENV GIT_AUTHOR_NAME=Braid \
    GIT_AUTHOR_EMAIL=braid@braid.local \
    GIT_COMMITTER_NAME=Braid \
    GIT_COMMITTER_EMAIL=braid@braid.local

WORKDIR /work
EXPOSE 8080
# Zone is chosen at runtime via BRAID_ZONE (injected by the Container class).
CMD ["braid", "serve", "-addr", ":8080"]
