FROM golang:latest as build

WORKDIR /build

COPY . .

RUN CGO_ENABLED=0 go build -ldflags="-s -w"

FROM scratch

WORKDIR /pacoloco

COPY --from=build /build/pacoloco .

EXPOSE 9129

CMD ["/pacoloco/pacoloco"]
