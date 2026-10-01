# syntax=docker/dockerfile:1.7
FROM rust:1.95-alpine AS source
RUN apk add --no-cache build-base
ENV LIBSQLITE3_FLAGS="-DHAVE_FDATASYNC=1"
WORKDIR /src
COPY Cargo.toml Cargo.lock ./
COPY src ./src

FROM source AS build
RUN --mount=type=cache,target=/usr/local/cargo/registry,sharing=locked \
    --mount=type=cache,target=/src/target,sharing=locked \
    cargo clean --release --package rfd-bot \
    && cargo build --locked --release --bin rfd-bot \
    && cp target/release/rfd-bot /rfd-bot \
    && readelf -h /rfd-bot >/dev/null \
    && test "$(readelf -d /rfd-bot | grep -c '(NEEDED)' || true)" = 0 \
    && test "$(readelf -l /rfd-bot | grep -c 'INTERP' || true)" = 0 \
    && mkdir /data && chown 65532:65532 /data

FROM build AS fixture-build
COPY examples ./examples
RUN --mount=type=cache,target=/usr/local/cargo/registry,sharing=locked \
    --mount=type=cache,target=/src/target,sharing=locked \
    cargo clean --release --package rfd-bot \
    && cargo build --locked --release --example runtime-fixture \
    && cp target/release/examples/runtime-fixture /runtime-fixture
FROM scratch AS fixture
COPY --from=fixture-build /runtime-fixture /runtime-fixture
USER 65532:65532
ENTRYPOINT ["/runtime-fixture"]

FROM source AS cloud-build
RUN --mount=type=cache,target=/usr/local/cargo/registry,sharing=locked \
    --mount=type=cache,target=/src/target,sharing=locked \
    cargo clean --release --package rfd-bot \
    && cargo build --locked --release --features gcp --bin rfd-bot \
    && cp target/release/rfd-bot /cloud-bot \
    && test "$(readelf -d /cloud-bot | grep -c '(NEEDED)' || true)" = 0 \
    && test "$(readelf -l /cloud-bot | grep -c 'INTERP' || true)" = 0

FROM scratch AS cloud-run
COPY --from=cloud-build /cloud-bot /bot
USER 65532:65532
ENTRYPOINT ["/bot"]
CMD ["cloud-run"]

FROM scratch AS runtime
COPY --from=build /rfd-bot /rfd-bot
COPY --from=build --chown=65532:65532 /data /data
USER 65532:65532
WORKDIR /data
ENV SQLITE_PATH=/data/rfd.sqlite LISTEN_ADDR=127.0.0.1:8080
ENTRYPOINT ["/rfd-bot"]
CMD ["run"]
