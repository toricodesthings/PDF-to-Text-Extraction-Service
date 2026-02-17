FROM golang:1.25.6-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/fileproc ./cmd/server

FROM debian:bookworm-slim
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends --no-install-suggests \
    poppler-utils \
    ca-certificates \
    ffmpeg \
    libreoffice-core \
    libreoffice-writer \
    libreoffice-calc \
    libreoffice-impress \
 && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /tmp/.libreoffice && \
    soffice --headless --convert-to txt --outdir /tmp /dev/null 2>/dev/null || true

WORKDIR /app
COPY --from=build /out/fileproc /app/fileproc
ENV PORT=8080
EXPOSE 8080
CMD ["/app/fileproc"]
