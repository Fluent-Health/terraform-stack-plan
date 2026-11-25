# Build a static tfstackplan and ship a minimal image whose entrypoint is the
# server. The Go binary is CGO-free (pure-Go SQLite) and embeds its migrations,
# so the runtime image needs no extra files. tfstackplan is the Rust front door
# (static musl); it runs tfstackplan-go for every subcommand not ported yet.
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/tfstackplan-go ./cmd/tfstackplan

FROM rust:1.98.1-slim-trixie AS rust
WORKDIR /src
COPY rust ./
RUN target="$(uname -m)-unknown-linux-musl" \
  && rustup target add "$target" \
  && cargo build --release --locked -p tfstackplan --target "$target" \
  && cp "target/${target}/release/tfstackplan" /tfstackplan

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=rust /tfstackplan /usr/local/bin/tfstackplan
COPY --from=build /out/tfstackplan-go /usr/local/bin/tfstackplan-go
EXPOSE 8080
ENTRYPOINT ["tfstackplan", "serve"]
