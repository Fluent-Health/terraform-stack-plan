# Build a static tfstackplan and ship a minimal image whose entrypoint is the
# server. The binary is CGO-free (pure-Go SQLite) and embeds its migrations, so
# the runtime image needs no extra files.
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/tfstackplan ./cmd/tfstackplan

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/tfstackplan /usr/local/bin/tfstackplan
EXPOSE 8080
ENTRYPOINT ["tfstackplan", "serve"]
