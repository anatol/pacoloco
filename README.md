# Pacoloco - caching proxy server for pacman

Pacoloco is a web server that acts if it was an Arch Linux pacman repository.
Every time pacoloco server gets a request from user it downloads this file from
real Arch Linux mirror and bypasses it to the user. Additionally pacoloco
saves this file to local filesystem cache and serves it to the future users.
It also allows to prefetch updates of the most recently used packages.

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

### Arch systems

Install [pacoloco package](https://archlinux.org/packages/community/x86_64/pacoloco/) from the official Arch repository.
Then start its systemd service: `# systemctl start pacoloco`.

### Docker

Pacoloco can be used with docker. See the docker.md file.

## Build from sources

Optionally you can build the binary from sources using `go build` command.

## Configure

### pacoloco.conf
The server configuration is located at `/etc/pacoloco.yaml`. Here is an example how the config file looks like:

```yaml
port: 9129
cache_dir: /var/cache/pacoloco
purge_files_after: 360000 # 360000 seconds or 100 hours, 0 to disable
download_timeout: 3600 # download will timeout after 3600 seconds
repos:
  archlinux:
    urls:
      - http://mirror.lty.me/archlinux
      - http://mirrors.kernel.org/archlinux
  quarry:
    url: http://pkgbuild.com/~anatolik/quarry/x86_64
  sublime:
    http_proxy: http://bar.company.com:8989 # Proxy could be enabled per-repo, shadowing the global `http_proxy` (see below)
    url: https://download.sublimetext.com/arch/stable/x86_64
  archlinux-reflector:
    mirrorlist: /etc/pacman.d/reflector_mirrorlist # Be careful! Check that pacoloco URL is NOT included in that file!
http_proxy: http://foo.company.com:8989 # Enable this only if you have pacoloco running behind a proxy
user_agent: Pacoloco/1.2
prefetch: # optional section, add it if you want to enable prefetching
  cron: 0 0 3 * * * * # standard cron expression (https://en.wikipedia.org/wiki/Cron#CRON_expression) to define how frequently prefetch, see https://github.com/gorhill/cronexpr#implementation for documentation.
  ttl_unaccessed_in_days: 30  # defaults to 30, set it to a higher value than the number of consecutive days you don't update your systems
  # It deletes and stop prefetch packages(and db links) when not downloaded after ttl_unaccessed_in_days days that it had been updated.
  ttl_unupdated_in_days: 300 # defaults to 300, it deletes and stop prefetch packages which hadn't been either updated upstream or requested for ttl_unupdated_in_days.
```

* `cache_dir` is the cache directory, this location needs to read/writable by the server process.
* `purge_files_after` specifies inactivity duration (in seconds) after which the file should be removed from the cache. This functionality uses unix "AccessTime" field to find out inactive files. Default value is `0` that means never run the purging.
* `port` is the server port.
* `download_timeout` is a timeout (in seconds) for internet->cache downloads. If a remote server gets slow and file download takes longer than this will be terminated. Default value is `0` that means no timeout.
* `repos` is a list of repositories to mirror. Each repo needs `name` and url of its Arch mirrors. Note that url can be specified either with `url` or `urls` properties, one and only one can be used for each repo configuration. Each repo could have its own `http_proxy`, which would shadow the global `http_proxy` (see below).
* `http_proxy` is only to be used if you have pacoloco running behind a proxy
* `user_agent` user agent used to fetch the files from repositories. Default value is `Pacoloco/1.2`.
* The `prefetch` section allows to enable packages prefetching. Comment it out to disable it.
* To test out if the cron value does what you'd expect to do, check cronexpr [implementation](https://github.com/gorhill/cronexpr#implementation) or [test it](https://play.golang.org/p/IK2hrIV7tUk)
* For what regards `mirrorlist`, be sure that pacoloco itself is NOT included in the chosen `mirrorlist` file. It can be integrated with reflector too, either by changing reflector's output path or by including pacoloco directly for standard repos in `/etc/pacman.conf` (e.g. adding a `Server=...` entry or a custom mirrorlist file which includes only pacoloco URL).

With the example configured above `http://YOURSERVER:9129/repo/archlinux` looks exactly like an Arch pacman mirror.
For example a request to `http://YOURSERVER:9129/repo/archlinux/core/os/x86_64/openssh-8.2p1-3-x86_64.pkg.tar.zst` will be served with file content from `http://mirror.lty.me/archlinux/core/os/x86_64/openssh-8.2p1-3-x86_64.pkg.tar.zst`

### pacman.conf
Once the pacoloco server is up and running, it is time to configure *pacman* to use *pacoloco*. Modify `/etc/pacman.conf` by changing the server it uses as mirror.

```conf
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

```conf
Server = http://yourpacoloco:9129/repo/archlinux/$repo/os/$arch
```

### Checklist
Run `pacman -Syu` on your client machine and check everything works smoothly. Also, check *pacoloco*'s log (with `journalctl -u pacoloco -e`), to see if the packages are downloaded and served as expected. You should see the same packages that have been upgraded with the previous pacman command run.

### CacheServer option
Since *pacman* **6.1**, it is also possible to use the *CacheServer* option. This allows *pacman* to fall-back on using the (main) mirror(s), if *pacoloco* is not available, like when your are not on your LAN, for example.

This time, you can keep your current mirror with the Server option, but instead, add a *CacheServer* option pointing at *pacoloco*, like that:

```conf
Server = https://youroriginalmirror/$repo/os/$arch
CacheServer = http://yourpacoloco:9129/repo/archlinux/$repo/os/$arch
```

You still need to specify a mirror in *pacoloco* config file, of course. It's better to use the same one.

Please refer to *pacman.conf* man page to learn more about this option.

That's it. From now on, pacman requests will be proxied through our pacoloco server.

## Handling multiple architectures

*pacoloco* does not care about the architecture of your repo as it acts as a mere proxy.

Thus it can handle multiple different arches transparently. One way to do it is to add multiple
repositories with names `foobar_$arch` e.g.:

```yaml
repos:
  archlinux_x86_64:
    urls:
      - http://mirror.lty.me/archlinux
      - http://mirrors.kernel.org/archlinux
  archlinux_armv7h:
    url: http://mirror.archlinuxarm.org
  archlinux_x86:
    url: http://mirror.clarkson.edu/archlinux32
```

Then modify user's `/etc/pacman.d/mirrorlist` and add

For x86_64:

```yaml
Server = http://yourpacoloco:9129/repo/archlinux_$arch/$repo/os/$arch
```

For armv7h:

```yaml
Server = http://yourpacoloco:9129/repo/archlinux_$arch/$arch/$repo
```

For x86:

```yaml
Server = http://yourpacoloco:9129/repo/archlinux_$arch/$arch/$repo
```

Please note that `archlinux_$arch` is the repo name in pacoloco.yaml.

## Credits

Huge thanks to all the people who contributed to this project! Pacoloco would not be able to become successful without your help.

<a href="https://github.com/anatol/pacoloco/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=anatol/pacoloco" />
</a>
