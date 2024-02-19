FROM golang:alpine3.19 AS common

RUN apk add gcc libc-dev

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./

FROM common AS test
RUN go test -ldflags="-s -w"

FROM common AS build
RUN go build -ldflags="-s -w"

FROM alpine:3.19 AS executable

RUN apk add tzdata

WORKDIR /pacoloco

COPY --from=build /build/pacoloco .

EXPOSE 9129

CMD ["/pacoloco/pacoloco"]
