FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/omnimcp ./cmd/omnimcp

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends chromium ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /out/omnimcp /app/omnimcp
COPY --from=build /src/README.md /app/README.md
VOLUME ["/app/data"]
EXPOSE 8090
ENTRYPOINT ["/app/omnimcp"]
