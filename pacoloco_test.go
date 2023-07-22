package main

import (
	"io"
	"log"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	if os.Getenv("TEST_VERBOSE") != "1" {
		// disable log output
		log.SetOutput(io.Discard)
	}

	os.Exit(m.Run())
}

func TestPathMatcher(t *testing.T) {
	pathCheck := func(url string, repoName string, path string, fileName string) {
		matches := pathRegex.FindStringSubmatch(url)
		require.NotNilf(t, matches, "url '%v' does not match regexp", url)
		expected := []string{url, repoName, path, fileName}

		require.Equal(t, expected, matches)
	}

	pathFails := func(url string) {
		matches := pathRegex.FindStringSubmatch(url)
		require.Nil(t, matches)
	}

	pathFails("")
	pathFails("/repofoo/webkit2gtk-2.26.4-1-x86_64.pkg.tar.zst")
	pathFails("repo/foo/webkit2gtk-2.26.4-1-x86_64.pkg.tar.zst")
	pathFails("/repo/webkit2gtk-2.26.4-1-x86_64.pkg.tar.zst")
	pathFails("/webkit2gtk/repo/foo/-2.26.4-1-x86_64.pkg.tar.zst")

	pathCheck("/repo/foo/webkit2gtk-2.26.4-1-x86_64.pkg.tar.zst", "foo", "", "webkit2gtk-2.26.4-1-x86_64.pkg.tar.zst")
	pathCheck("/repo/foo/bar/bazzz/eeee/webkit2x86_64.pkg.tar.zst", "foo", "/bar/bazzz/eeee", "webkit2x86_64.pkg.tar.zst")
}

func TestForceCheckAtServer(t *testing.T) {
	forceCheck := func(name string) {
		require.Truef(t, forceCheckAtServer(name), "File '%v' expected to force check at server", name)
	}
	doNotForceCheck := func(name string) {
		require.Falsef(t, forceCheckAtServer(name), "File '%v' expected to not force check at server", name)
	}

	forceCheck("core.db")
	forceCheck("core.db.sig")
	forceCheck("core.files")

	doNotForceCheck("core.1db")
	doNotForceCheck("core.db.test")
}
