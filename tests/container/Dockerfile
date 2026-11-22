# This image is only the HTTPS API fixture; the application under test is
# built separately from the repository's real Dockerfile.
FROM golang:1.26-alpine3.22 AS build
WORKDIR /src
COPY tests/container/fixture/main.go ./main.go
RUN CGO_ENABLED=0 go build -tags=container_smoke -o /fixture main.go

FROM alpine:3.22
# Only the test fixture needs SQLite tooling to seed and inspect legacy state.
RUN apk add --no-cache sqlite
COPY --from=build /fixture /fixture
ENTRYPOINT ["/fixture"]
