package main

import (
	"github.com/google/go-cmp/cmp"
	"testing"
)

func TestPathMatcher(t *testing.T) {
	pathCheck := func(url string, repoName string, path string, fileName string) {
		matches := pathRegex.FindStringSubmatch(url)
		if matches == nil {
			t.Errorf("url '%v' does not match regexp", url)
		}
		expected := []string{url, repoName, path, fileName}

		if !cmp.Equal(matches, expected) {
			t.Errorf("expected '%v' but regexp submatches '%v'", expected, matches)
		}
	}

	pathFails := func(url string) {
		matches := pathRegex.FindStringSubmatch(url)
		if matches != nil {
			t.Errorf("url '%v' expected to fail matching", url)
		}
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
		if !forceCheckAtServer(name) {
			t.Errorf("File '%v' expected to force check at server", name)
		}
	}
	doNotForceCheck := func(name string) {
		if forceCheckAtServer(name) {
			t.Errorf("File '%v' expected to not force check at server", name)
		}
	}

	forceCheck("core.db")
	forceCheck("core.db.sig")
	forceCheck("core.files")

	doNotForceCheck("core.1db")
	doNotForceCheck("core.db.test")
}
