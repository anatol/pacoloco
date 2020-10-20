package main

import (
	"reflect"
	"testing"
)

// test that `parseConfig()` can successfully load YAML config
func TestLoadConfig(t *testing.T) {
	parseConfig([]byte(`
port: 9129
cache_dir: /tmp
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
		Repos: map[string]Repo{
			"archlinux": Repo{
				Url: "http://mirrors.kernel.org/archlinux",
			},
		},
		PurgeFilesAfter: 2592000,
		DownloadTimeout: 0,
	}

	if !reflect.DeepEqual(got, want) {
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
		Repos: map[string]Repo{
			"archlinux": Repo{
				Url: "http://mirrors.kernel.org/archlinux",
			},
		},
		PurgeFilesAfter: 0,
		DownloadTimeout: 0,
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", *got, *want)
	}
}
