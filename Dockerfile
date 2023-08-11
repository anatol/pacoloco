FROM golang:alpine3.18 as build

RUN apk add gcc libc-dev

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./

RUN go build -ldflags="-s -w"

FROM alpine:3.18

RUN apk add tzdata

WORKDIR /pacoloco

COPY --from=build /build/pacoloco .

EXPOSE 9129

CMD ["/pacoloco/pacoloco"]
