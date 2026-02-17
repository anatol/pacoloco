package main

import (
	"os"
	"path"
	"testing"

	"github.com/stretchr/testify/require"
)

// test that `parseConfig()` can successfully load YAML config
func TestLoadConfig(t *testing.T) {
	temp := t.TempDir()
	_, err := parseConfig([]byte(`
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
	require.NoError(t, err)
}

// test with prefetch set
func TestLoadConfigWithPrefetch(t *testing.T) {
	got, err := parseConfig([]byte(`
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
	require.NoError(t, err)
	want := &Config{
		CacheDir: `/tmp`,
		Port:     9139,
		Repos: map[string]*Repo{
			"archlinux": {
				URL: "http://mirrors.kernel.org/archlinux",
			},
		},
		PurgeFilesAfter: 2592000,
		DownloadTimeout: 200,
		Prefetch:        &RefreshPeriod{Cron: "0 0 3 * * * *", TTLUnaccessed: 5, TTLUnupdated: 200},
	}
	require.Equal(t, want, got)
}

// test that `purgeFilesAfter` is being read correctly
func TestPurgeFilesAfter(t *testing.T) {
	got, err := parseConfig([]byte(`
cache_dir: /tmp
purge_files_after: 2592000 # 3600 * 24 * 30days
repos:
  archlinux:
    url: http://mirrors.kernel.org/archlinux
`))
	require.NoError(t, err)
	want := &Config{
		CacheDir: `/tmp`,
		Port:     9129,
		Repos: map[string]*Repo{
			"archlinux": {
				URL: "http://mirrors.kernel.org/archlinux",
			},
		},
		PurgeFilesAfter: 2592000,
		DownloadTimeout: 0,
		Prefetch:        nil,
	}

	require.Equal(t, want, got)
}

// test that config works without `purgeFilesAfter`
func TestWithoutPurgeFilesAfter(t *testing.T) {
	got, err := parseConfig([]byte(`
cache_dir: /tmp
repos:
  archlinux:
    url: http://mirrors.kernel.org/archlinux
`))
	require.NoError(t, err)
	want := &Config{
		CacheDir: `/tmp`,
		Port:     9129,
		Repos: map[string]*Repo{
			"archlinux": {
				URL: "http://mirrors.kernel.org/archlinux",
			},
		},
		PurgeFilesAfter: 0,
		DownloadTimeout: 0,
		Prefetch:        nil,
	}

	require.Equal(t, want, got)
}

func TestLoadConfigWithMirrorlist(t *testing.T) {
	temp := t.TempDir()
	tmpfile := path.Join(temp, "tmpMirrorFile")

	f, err := os.Create(tmpfile)
	require.NoError(t, err)
	f.Close()
	got, err := parseConfig([]byte(`
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
	require.NoError(t, err)
	want := &Config{
		CacheDir: temp,
		Port:     9139,
		Repos: map[string]*Repo{
			"archlinux": {
				Mirrorlist: tmpfile,
			},
		},
		PurgeFilesAfter: 2592000,
		DownloadTimeout: 200,
		Prefetch:        &RefreshPeriod{Cron: "0 0 3 * * * *", TTLUnaccessed: 5, TTLUnupdated: 200},
	}
	require.Equal(t, want, got)
}

func TestLoadConfigWithMirrorlistTimestamps(t *testing.T) {
	got, err := parseConfig([]byte(`
cache_dir: /tmp
repos:
  archlinux:
    url: http://mirrors.kernel.org/archlinux
    # these fields *shouldn't* be unmarshalled
    lastmirrorlistcheck: 2
    lastmodificationtime: 2
`))
	require.NoError(t, err)
	want := &Config{
		CacheDir: "/tmp",
		Port:     DefaultPort,
		Repos: map[string]*Repo{
			"archlinux": {
				URL: "http://mirrors.kernel.org/archlinux",
			},
		},
	}
	require.Equal(t, want, got)
}

// test with Tls enabled
func TestLoadConfigWithTls(t *testing.T) {
	got, err := parseConfig([]byte(`
cache_dir: /tmp
download_timeout: 200
port: 9139
tls:
  key: config_test.go
  cert: config_test.go
repos:
  archlinux:
    url: http://mirrors.kernel.org/archlinux

`))
	require.NoError(t, err)
	want := &Config{
		CacheDir: `/tmp`,
		Port:     9139,
		Repos: map[string]*Repo{
			"archlinux": {
				URL: "http://mirrors.kernel.org/archlinux",
			},
		},
		DownloadTimeout: 200,
		Tls: &Tls{
			Key:         "config_test.go",
			Certificate: "config_test.go",
		},
	}
	require.Equal(t, want, got)
}

// Error path tests

func TestParseConfigInvalidYAML(t *testing.T) {
	_, err := parseConfig([]byte(`
cache_dir: /tmp
repos:
  - this is not valid yaml mapping
  archlinux:
`))
	require.Error(t, err)
}

func TestParseConfigBothURLAndURLs(t *testing.T) {
	_, err := parseConfig([]byte(`
cache_dir: /tmp
repos:
  archlinux:
    url: http://mirror1.example.com/archlinux
    urls:
      - http://mirror2.example.com/archlinux
`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "both url and urls")
}

func TestParseConfigBothURLAndMirrorlist(t *testing.T) {
	temp := t.TempDir()
	tmpfile := path.Join(temp, "mirrorlist")
	require.NoError(t, os.WriteFile(tmpfile, []byte("Server = http://example.com/$repo/os/$arch"), 0o644))

	_, err := parseConfig([]byte(`
cache_dir: ` + temp + `
repos:
  archlinux:
    url: http://mirror1.example.com/archlinux
    mirrorlist: ` + tmpfile + `
`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "both url and mirrorlist")
}

func TestParseConfigNoURLs(t *testing.T) {
	_, err := parseConfig([]byte(`
cache_dir: /tmp
repos:
  archlinux: {}
`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "specify url(s) or mirrorlist")
}

func TestParseConfigPurgeFilesAfterTooLow(t *testing.T) {
	_, err := parseConfig([]byte(`
cache_dir: /tmp
purge_files_after: 100
repos:
  archlinux:
    url: http://mirrors.kernel.org/archlinux
`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "purge_files_after")
}

func TestParseConfigInvalidCron(t *testing.T) {
	_, err := parseConfig([]byte(`
cache_dir: /tmp
prefetch:
  cron: not-a-valid-cron
repos:
  archlinux:
    url: http://mirrors.kernel.org/archlinux
`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "cron")
}

func TestParseConfigNegativeTTLUnaccessed(t *testing.T) {
	_, err := parseConfig([]byte(`
cache_dir: /tmp
prefetch:
  cron: 0 0 3 * * * *
  ttl_unaccessed_in_days: -1
repos:
  archlinux:
    url: http://mirrors.kernel.org/archlinux
`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "ttl_unaccessed_in_days")
}

func TestParseConfigNegativeTTLUnupdated(t *testing.T) {
	_, err := parseConfig([]byte(`
cache_dir: /tmp
prefetch:
  cron: 0 0 3 * * * *
  ttl_unupdated_in_days: -5
repos:
  archlinux:
    url: http://mirrors.kernel.org/archlinux
`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "ttl_unupdated_in_days")
}

func TestParseConfigInvalidCacheDir(t *testing.T) {
	_, err := parseConfig([]byte(`
cache_dir: /nonexistent/path/that/does/not/exist
repos:
  archlinux:
    url: http://mirrors.kernel.org/archlinux
`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not exist or isn't writable")
}

func TestParseConfigInvalidTLSPaths(t *testing.T) {
	_, err := parseConfig([]byte(`
cache_dir: /tmp
tls:
  cert: /nonexistent/cert.pem
  key: /nonexistent/key.pem
repos:
  archlinux:
    url: http://mirrors.kernel.org/archlinux
`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "tls cert file")
}

func TestParseConfigInvalidTLSKey(t *testing.T) {
	_, err := parseConfig([]byte(`
cache_dir: /tmp
tls:
  cert: config_test.go
  key: /nonexistent/key.pem
repos:
  archlinux:
    url: http://mirrors.kernel.org/archlinux
`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "tls key file")
}
