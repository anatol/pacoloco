package main

import (
	"os"
	"path"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// test that `parseConfig()` can successfully load YAML config
func TestLoadConfig(t *testing.T) {
	var temp = t.TempDir()
	parseConfig([]byte(`
port: 9129
cache_dir: ` + temp + `
purge_files_after: 2592000 # 3600 * 24 * 30days
download_timeout: 200 # 200 seconds
repos:
  archlinux:
    urls:
      - http://mirror.lty.me/archlinux
      - http://mirrors.kernel.org/archlinux
  quarry:
    url: http://pkgbuild.com/~anatolik/quarry/x86_64
  sublime:
    url: https://download.sublimetext.com/arch/stable/x86_64
`))
}

// test with prefetch set
func TestLoadConfigWithPrefetch(t *testing.T) {
	got := parseConfig([]byte(`
cache_dir: /tmp
purge_files_after: 2592000 # 3600 * 24 * 30days
prefetch:
  cron: 0 0 3 * * * *
  ttl_unaccessed_in_days: 5
download_timeout: 200
port: 9139
repos:
  archlinux:
    url: http://mirrors.kernel.org/archlinux

`))
	want := &Config{
		CacheDir: `/tmp`,
		Port:     9139,
		Repos: map[string]*Repo{
			"archlinux": &Repo{
				URL: "http://mirrors.kernel.org/archlinux",
			},
		},
		PurgeFilesAfter: 2592000,
		DownloadTimeout: 200,
		Prefetch:        &RefreshPeriod{Cron: "0 0 3 * * * *", TTLUnaccessed: 5, TTLUnupdated: 200},
	}
	if !cmp.Equal(*got, *want, cmpopts.IgnoreFields(Config{}, "Prefetch"), cmpopts.IgnoreUnexported(Repo{})) {
		t.Errorf("got %v, want %v", *got, *want)
	}
	gotR := *(*got).Prefetch
	wantR := *(*want).Prefetch
	if !cmp.Equal(gotR, wantR) {
		t.Errorf("got %v, want %v", gotR, wantR)
	}
}

// test that `purgeFilesAfter` is being read correctly
func TestPurgeFilesAfter(t *testing.T) {
	got := parseConfig([]byte(`
cache_dir: /tmp
purge_files_after: 2592000 # 3600 * 24 * 30days
repos:
  archlinux:
    url: http://mirrors.kernel.org/archlinux
`))
	want := &Config{
		CacheDir: `/tmp`,
		Port:     9129,
		Repos: map[string]*Repo{
			"archlinux": &Repo{
				URL: "http://mirrors.kernel.org/archlinux",
			},
		},
		PurgeFilesAfter: 2592000,
		DownloadTimeout: 0,
		Prefetch:        nil,
	}

	if !cmp.Equal(*got, *want, cmpopts.IgnoreUnexported(Repo{})) {
		t.Errorf("got %v, want %v", *got, *want)
	}
}

// test that config works without `purgeFilesAfter`
func TestWithoutPurgeFilesAfter(t *testing.T) {
	got := parseConfig([]byte(`
cache_dir: /tmp
repos:
  archlinux:
    url: http://mirrors.kernel.org/archlinux
`))
	want := &Config{
		CacheDir: `/tmp`,
		Port:     9129,
		Repos: map[string]*Repo{
			"archlinux": &Repo{
				URL: "http://mirrors.kernel.org/archlinux",
			},
		},
		PurgeFilesAfter: 0,
		DownloadTimeout: 0,
		Prefetch:        nil,
	}

	if !cmp.Equal(*got, *want, cmpopts.IgnoreUnexported(Repo{})) {
		t.Errorf("got %v, want %v", *got, *want)
	}
}

func TestLoadConfigWithMirrorlist(t *testing.T) {
	var temp = t.TempDir()
	var tmpfile = path.Join(temp, "tmpMirrorFile")

	f, err := os.Create(tmpfile)
	if err != nil {
		t.Error(err)
	}
	f.Close()
	got := parseConfig([]byte(`
cache_dir: ` + temp + `
purge_files_after: 2592000 # 3600 * 24 * 30days
prefetch:
  cron: 0 0 3 * * * *
  ttl_unaccessed_in_days: 5
download_timeout: 200
port: 9139
repos:
  archlinux:
    mirrorlist: ` + tmpfile + `

`))
	want := &Config{
		CacheDir: temp,
		Port:     9139,
		Repos: map[string]*Repo{
			"archlinux": &Repo{
				Mirrorlist: tmpfile,
			},
		},
		PurgeFilesAfter: 2592000,
		DownloadTimeout: 200,
		Prefetch:        &RefreshPeriod{Cron: "0 0 3 * * * *", TTLUnaccessed: 5, TTLUnupdated: 200},
	}
	if !cmp.Equal(*got, *want, cmpopts.IgnoreFields(Config{}, "Prefetch"), cmpopts.IgnoreUnexported(Repo{})) {
		t.Errorf("got %v, want %v", *got, *want)
	}
	gotR := *(*got).Prefetch
	wantR := *(*want).Prefetch
	if !cmp.Equal(gotR, wantR) {
		t.Errorf("got %v, want %v", gotR, wantR)
	}
}

func TestLoadConfigWithMirrorlistTimestamps(t *testing.T) {
	got := parseConfig([]byte(`
cache_dir: /tmp
repos:
  archlinux:
    url: http://mirrors.kernel.org/archlinux
    # these fields *shouldn't* be unmarshalled
    lastmirrorlistcheck: 2
    lastmodificationtime: 2
`))
	want := &Config{
		CacheDir: "/tmp",
		Port:     DefaultPort,
		Repos: map[string]*Repo{
			"archlinux": &Repo{
				URL: "http://mirrors.kernel.org/archlinux",
			},
		},
	}
	if !cmp.Equal(*got, *want, cmpopts.IgnoreUnexported(Repo{})) {
		t.Errorf("got %v, want %v", *got, *want)
	}
}
