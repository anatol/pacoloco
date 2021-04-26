FROM golang:latest

WORKDIR /pacoloco

COPY . .

RUN go build

RUN useradd pacoloco
RUN mkdir -p /var/cache/pacoloco/pkgs
RUN chown -R pacoloco:pacoloco /var/cache/pacoloco/

USER pacoloco

CMD ./pacoloco
