FROM rust:1.86-bookworm AS build
WORKDIR /build
COPY Cargo.toml Cargo.lock README.md ./
COPY rust ./rust
COPY docs/rust.md ./docs/rust.md
RUN cargo build --locked --release

FROM debian:bookworm-slim
RUN useradd --uid 10001 --create-home filewise && mkdir /data && chown filewise /data && chmod 700 /data
COPY --from=build /build/target/release/filewise /usr/local/bin/filewise
USER filewise
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8000
ENTRYPOINT ["filewise"]
CMD ["--db", "/data/filewise-rust.db", "serve", "--host", "0.0.0.0", "--tokens", "/run/secrets/filewise_tokens"]
