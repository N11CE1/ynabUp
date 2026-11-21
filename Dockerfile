FROM golang:1.26-alpine3.22 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 works cleanly here because modernc.org/sqlite is a pure-Go
# SQLite driver - no C toolchain needed for a static binary.
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/ynabup .

FROM alpine:3.22

RUN apk add --no-cache ca-certificates su-exec wget \
	&& addgroup -S ynabup \
	&& adduser -S -G ynabup -h /app ynabup \
	&& mkdir -p /app /data \
	&& chown -R ynabup:ynabup /app /data

WORKDIR /app
COPY --from=build /out/ynabup /app/ynabup
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh \
	&& chown ynabup:ynabup /app/ynabup

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["-serve"]
