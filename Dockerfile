FROM golang:latest as build

WORKDIR /build

COPY . .

RUN go build -ldflags="-s -w"

FROM scratch

WORKDIR /pacoloco

COPY --from=build /build/pacoloco .

EXPOSE 9129

CMD ["/pacoloco/pacoloco"]
