FROM golang:1.27.1-bookworm AS build
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /filewise ./cmd/filewise

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --create-home filewise && mkdir /data && chown filewise /data && chmod 700 /data
COPY --from=build /filewise /usr/local/bin/filewise
USER filewise
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8000
ENTRYPOINT ["filewise"]
CMD ["--db", "/data/filewise-go.db", "serve", "--host", "0.0.0.0", "--tokens", "/run/secrets/filewise_tokens"]
