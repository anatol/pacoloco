# Docker

Pacoloco can be used with docker.

## Prebuilt image
You can get a prebuilt image from GitHub's [container registry](https://github.com/anatol/pacoloco/pkgs/container/pacoloco) (see also sidebar).
Currently the images are built for `amd64` and ARM (`arm64`, `armv7`) architectures.
```sh
docker pull ghcr.io/anatol/pacoloco
```
Available tags are: `latest` = git master and any git tags.

## Buidling one yourself
You can also build pacoloco docker image yourself:
```sh
$ git clone https://github.com/anatol/pacoloco && cd pacoloco
$ docker build -t ghcr.io/anatol/pacoloco .
```

Run it like this:
```sh
$ docker run -p 9129:9129 \
    -v /path/to/config/pacoloco.yaml:/etc/pacoloco.yaml \
    -v /path/to/cache:/var/cache/pacoloco \
    ghcr.io/anatol/pacoloco
```
You need to provide paths or volumes to store application data.

Alternatively, you can use docker-compose:
```yaml
---
version: "3.8"
services:
  pacoloco:
#   if a specific user id is provided, you have to make sure
#   the mounted directories have the same user id owner on host
#   user: 1000:1000
    container_name: pacoloco
#   to pull the image from github's registry:
    image: ghcr.io/anatol/pacoloco
#   or replace it for for self-building with:
#    build: https://github.com/anatol/pacoloco.git
    ports:
      - "9129:9129"
    volumes:
      - /path/to/cache:/var/cache/pacoloco
      - /path/to/config/pacoloco.yaml:/etc/pacoloco.yaml
    restart: unless-stopped
#   to set time zone within the container for cron and log timestamps:
#    environment:
#      - TZ=Europe/Berlin
```

