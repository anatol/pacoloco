# Pacoloco - caching proxy server for pacman
Pacoloco is a web server that acts if it was an Arch Linux pacman repository.
Every time pacoloco server gets a request from user it downloads this file from
real Arch Linux mirror and bypasses it to the user. Additionally pacoloco
saves this file to local filesystem cache and serves it to the future users.

## How does it help?
Fast internet is still a luxury in many parts of the world. There are many places
where access to internet is expensive and slow due to geographical and economical
reasons.

Now think about a situation when multiple pacman users connected via fast local network.
Each of these users needs to download the same set of files. _Pacoloco_ allows to minimize
the Internet workload by caching pacman files content and serving it over
fast local network.

_Pacoloco_ does not mirror the whole Arch repository. It only downloads files needed by local users.
You can think of pacoloco as a lazy Arch mirror.

## Install
Install [pacoloco-git package](https://aur.archlinux.org/packages/pacoloco-git/) from AUR repository.
Then start its systemd service: `# systemctl start pacoloco`.

## Build from sources
Optionally you can build the binary from sources using `go build` command.

## Configure
The server configuration is located at `/etc/pacoloco.yaml`. Here is an example how the config file looks like:

```
port: 9129
cache_dir: /var/cache/pacoloco
purge_files_after: 360000 # 360000 seconds or 100 hours
repos:
  archlinux:
    urls:
      - http://mirror.lty.me/archlinux
      - http://mirrors.kernel.org/archlinux
  quarry:
    url: http://pkgbuild.com/~anatolik/quarry/x86_64
  sublime:
    url: https://download.sublimetext.com/arch/stable/x86_64
```

`cache_dir` is the cache directory, this location needs to read/writable by the server process.
`purge_files_after` specifies inactivity duration (in seconds) after which the file should be removed from the cache. This functionality uses unix "AccessTime" field to find out inactive files.
`port` is the server port.
`repos` is a list of repositories to mirror. Each repo needs `name` and url of its Arch mirrors. Note that url can be specified either with `url` or `urls` properties, one and only one can be used for each repo configuration.

With the example configured above `http://YOURSERVER:9129/repo/archlinux` looks exactly like an Arch pacman mirror.
For example a request to `http://YOURSERVER:9129/repo/archlinux/core/os/x86_64/openssh-8.2p1-3-x86_64.pkg.tar.zst` will be served with file content from `http://mirror.lty.me/archlinux/core/os/x86_64/openssh-8.2p1-3-x86_64.pkg.tar.zst`

Once the pacoloco server is up and running it is time to configure the user host. Modify user's `/etc/pacman.conf` with

```
[core]
Include = /etc/pacman.d/mirrorlist

[extra]
Include = /etc/pacman.d/mirrorlist

[community]
Include = /etc/pacman.d/mirrorlist

[quarry]
Server = http://yourpacoloco:9129/repo/quarry

[sublime-text]
Server = http://yourpacoloco:9129/repo/sublime
```

And `/etc/pacman.d/mirrorlist` with
```
Server = http://yourpacoloco:9129/repo/archlinux/$repo/os/$arch
```

That's it. Since now pacman requests will be proxied through our pacoloco server.
