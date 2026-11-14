# Multi-stage build: frontend -> static Go binary -> minimal runtime image.
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
ARG VERSION=docker
ARG COMMIT=none
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/ydong/ssrservice/internal/version.Version=${VERSION} -X github.com/ydong/ssrservice/internal/version.Commit=${COMMIT}" \
    -o /out/sspanel ./cmd/sspanel

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S sspanel && adduser -S -G sspanel -h /var/lib/sspanel sspanel && \
    mkdir -p /var/lib/sspanel /etc/sspanel && \
    chown -R sspanel:sspanel /var/lib/sspanel
COPY --from=build /out/sspanel /usr/local/bin/sspanel
COPY deploy/config.docker.yaml /etc/sspanel/config.yaml
USER sspanel
WORKDIR /var/lib/sspanel
EXPOSE 2096
VOLUME ["/var/lib/sspanel"]
ENTRYPOINT ["/usr/local/bin/sspanel"]
CMD ["run", "--config", "/etc/sspanel/config.yaml"]
