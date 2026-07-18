FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 works cleanly here because modernc.org/sqlite is a pure-Go
# SQLite driver - no C toolchain needed for a static binary.
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/ynabup .

FROM alpine:latest

RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY --from=build /out/ynabup /app/ynabup

EXPOSE 8080

ENTRYPOINT ["/app/ynabup"]
CMD ["-serve"]
