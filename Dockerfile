FROM golang:alpine3.19 AS common

RUN apk add gcc libc-dev

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./

# CGO_CFLAGS to mitigate https://github.com/mattn/go-sqlite3/issues/1164

FROM common AS test
RUN CGO_CFLAGS="-D_LARGEFILE64_SOURCE" go test -ldflags="-s -w"

FROM common AS build
RUN CGO_CFLAGS="-D_LARGEFILE64_SOURCE" go build -ldflags="-s -w"

FROM alpine:3.19 AS executable

RUN apk add tzdata

WORKDIR /pacoloco

COPY --from=build /build/pacoloco .

EXPOSE 9129

CMD ["/pacoloco/pacoloco"]
