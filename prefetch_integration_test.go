package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPacolocoPrefetchIntegration(t *testing.T) {
	var err error
	mirrorDir, err = os.MkdirTemp(os.TempDir(), "*-pacoloco-mirror")
	require.NoError(t, err)
	defer os.RemoveAll(mirrorDir)

	// For easier setup we are going to serve several Arch mirror trees by one
	// instance of http.FileServer
	mirror := httptest.NewServer(http.FileServer(http.Dir(mirrorDir)))
	defer mirror.Close()
	mirrorURL = mirror.URL

	// Now setup pacoloco cache dir
	testPacolocoDir = testSetupHelper(t)
	// setup the prefetching
	setupPrefetch()
	defer os.RemoveAll(testPacolocoDir)

	pacoloco := httptest.NewServer(http.HandlerFunc(pacolocoHandler))
	defer pacoloco.Close()
	pacolocoURL = pacoloco.URL

	t.Run("testPrefetchInvalidURL", testPrefetchInvalidURL)
	t.Run("testPrefetchRequestNonExistingDb", testPrefetchRequestNonExistingDb)
	t.Run("testPrefetchRequestExistingRepo", testPrefetchRequestExistingRepo)
	t.Run("testPrefetchRequestExistingRepoWithDb", testPrefetchRequestExistingRepoWithDb)
	t.Run("testPrefetchRequestPackageFile", testPrefetchRequestPackageFile)
	t.Run("testPrefetchFailover", testPrefetchFailover)
	t.Run("testPrefetchRealDB", testPrefetchRealDB)
	t.Run("testIntegrationPrefetchAllPkgs", testIntegrationPrefetchAllPkgs)
}

func testPrefetchInvalidURL(t *testing.T) {
	require.Error(t, prefetchRequest("/foo", ""))
}

func testPrefetchRequestNonExistingDb(t *testing.T) {
	// Requesting non-existing repo
	require.Error(t, prefetchRequest("/repo/test/test.db", ""))

	// check that no repo cached
	_, err := os.Stat(path.Join(testPacolocoDir, "pkgs", "test"))
	require.ErrorIsf(t, err, os.ErrNotExist, "test repo should not be cached")
}

func testPrefetchRequestExistingRepo(t *testing.T) {
	// Requesting existing repo
	config.Repos["repo1"] = &Repo{}
	defer delete(config.Repos, "repo1")

	require.Error(t, prefetchRequest("/repo/repo1/test.db", ""))

	// check that db is not cached
	_, err := os.Stat(path.Join(testPacolocoDir, "pkgs", "repo1", "test.db"))
	require.ErrorIs(t, err, os.ErrNotExist, "repo1/test.db should not be cached")
}

func testPrefetchRequestPackageFile(t *testing.T) {
	// Requesting existing repo
	repo3 := &Repo{
		URL: mirrorURL + "/mirror3",
	}
	config.Repos["repo3"] = repo3
	defer delete(config.Repos, "repo3")

	require.NoError(t, os.Mkdir(path.Join(mirrorDir, "mirror3"), os.ModePerm))
	defer os.RemoveAll(path.Join(mirrorDir, "mirror3"))

	pkgAtMirror := path.Join(mirrorDir, "mirror3", "test-1-1-any.pkg.tar.zst")
	pkgFileContent := "a package"
	require.NoError(t, os.WriteFile(pkgAtMirror, []byte(pkgFileContent), os.ModePerm))
	// Make the mirror file old enough to distinguish it from the subsequent modifications
	pkgModTime := time.Now().Add(-time.Hour)
	os.Chtimes(pkgAtMirror, pkgModTime, pkgModTime)

	err := prefetchRequest("/repo/repo3/test-1-1-any.pkg.tar.zst", "")

	defer os.RemoveAll(path.Join(testPacolocoDir, "pkgs", "repo3")) // remove cached content

	require.NoError(t, err)
	content, err := os.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo3", "test-1-1-any.pkg.tar.zst"))
	require.NoError(t, err)
	require.Equal(t, string(content), pkgFileContent)

	// Now let's modify the db content, pacoloco should refetch it, cause prefetch should force update packages
	// This can also be useful to redownload packages with a wrong signature
	newContent := "This is a new content"
	require.NoError(t, os.WriteFile(pkgAtMirror, []byte(newContent), os.ModePerm))
	newDbModTime := time.Now()
	require.NoError(t, os.Chtimes(pkgAtMirror, newDbModTime, newDbModTime))

	err = prefetchRequest("/repo/repo3/test-1-1-any.pkg.tar.zst", "")

	require.NoError(t, err)

	content, err = os.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo3", "test-1-1-any.pkg.tar.zst"))
	require.NoError(t, err)
	require.Equal(t, string(content), pkgFileContent)
}

func testPrefetchFailover(t *testing.T) {
	failover := &Repo{
		URLs: []string{
			mirrorURL + "/no-mirror",
			mirrorURL + "/mirror-failover",
		},
	}
	config.Repos["failover"] = failover
	defer delete(config.Repos, "failover")

	require.NoError(t, os.Mkdir(path.Join(mirrorDir, "mirror-failover"), os.ModePerm))
	defer os.RemoveAll(path.Join(mirrorDir, "mirror-failover"))

	pkgAtMirror := path.Join(mirrorDir, "mirror-failover", "test-1-1-any.pkg.tar.zst")
	pkgFileContent := "failover content"
	require.NoError(t, os.WriteFile(pkgAtMirror, []byte(pkgFileContent), os.ModePerm))

	err := prefetchRequest("/repo/failover/test-1-1-any.pkg.tar.zst", "")
	require.NoError(t, err)
	defer os.RemoveAll(path.Join(testPacolocoDir, "pkgs", "failover")) // remove cached content

	content, err := os.ReadFile(path.Join(testPacolocoDir, "pkgs", "failover", "test-1-1-any.pkg.tar.zst"))
	require.NoError(t, err)
	require.Equal(t, string(content), pkgFileContent)
}

// prefetch an actual db and parses it
func testPrefetchRealDB(t *testing.T) {
	repo2 := &Repo{
		URL: mirrorURL + "/mirror2",
	}
	config.Repos["repo2"] = repo2
	defer delete(config.Repos, "repo2")

	require.NoError(t, os.Mkdir(path.Join(mirrorDir, "mirror2"), os.ModePerm))
	defer os.RemoveAll(path.Join(mirrorDir, "mirror2"))

	dbAtMirror := path.Join(mirrorDir, "mirror2", "test.db")
	createDbTarball(t, dbAtMirror, getTestTarDB())
	mirror, err := updateDBRequestedDB("repo2", "", "/test.db")
	require.NoError(t, err)
	err = downloadAndParseDb(mirror)
	require.NoErrorf(t, err, "This shouldn't fail at all. Error: %v", err)
}

func testPrefetchRequestExistingRepoWithDb(t *testing.T) {
	// Requesting existing repo
	repo2 := &Repo{
		URL: mirrorURL + "/mirror2",
	}
	config.Repos["repo2"] = repo2
	defer delete(config.Repos, "repo2")

	require.NoError(t, os.Mkdir(path.Join(mirrorDir, "mirror2"), os.ModePerm))
	defer os.RemoveAll(path.Join(mirrorDir, "mirror2"))

	dbAtMirror := path.Join(mirrorDir, "mirror2", "test.db")
	dbFileContent := "pacoloco/mirror2.db"

	require.NoError(t, os.WriteFile(dbAtMirror, []byte(dbFileContent), os.ModePerm))
	// Make the mirror file old enough to distinguish it from the subsequent modifications
	dbModTime := time.Now().Add(-time.Hour)
	os.Chtimes(dbAtMirror, dbModTime, dbModTime)

	err := prefetchRequest("/repo/repo2/test.db", "")
	require.NoError(t, err)

	content, err := os.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo2", "test.db"))
	require.NoError(t, err)
	require.Equal(t, string(content), dbFileContent)

	defer os.RemoveAll(path.Join(testPacolocoDir, "pkgs", "repo2"))
	content, err = os.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo2", "test.db"))
	require.NoError(t, err)
	require.Equal(t, string(content), dbFileContent)

	// Now let's modify the db content, pacoloco should refetch it
	dbFileContent = "This is a new content"
	require.NoError(t, os.WriteFile(dbAtMirror, []byte(dbFileContent), os.ModePerm))
	newDbModTime := time.Now()
	os.Chtimes(dbAtMirror, newDbModTime, newDbModTime)

	err = prefetchRequest("/repo/repo2/test.db", "")
	require.NoError(t, err)

	content, err = os.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo2", "test.db"))
	require.NoError(t, err)
	require.Equal(t, dbFileContent, string(content))
}

// The most complete integration test for prefetching
func testIntegrationPrefetchAllPkgs(t *testing.T) {
	// Setting up an existing repo
	repo3 := &Repo{
		URL: mirrorURL + "/mirror3",
	}
	config.Repos["repo3"] = repo3
	defer delete(config.Repos, "repo3")

	require.NoError(t, os.Mkdir(path.Join(mirrorDir, "mirror3"), os.ModePerm))
	defer os.RemoveAll(path.Join(mirrorDir, "mirror3"))
	// create a valid db file on the mirror
	dbAtMirror := path.Join(mirrorDir, "mirror3", "test.db")
	createDbTarball(t, dbAtMirror, getTestTarDB())
	// fake a request to the db
	_, err := updateDBRequestedDB("repo3", "", "/test.db")
	require.NoErrorf(t, err, "Should not generate errors, but got %v", err)
	// now add a fake older version of a package which is in the db
	updateDBRequestedFile("repo3", "acl-2.0-0-x86_64.pkg.tar.zst")
	// now i add a bit newer one, to ensure that the db gets updated accordingly
	updateDBRequestedFile("repo3", "acl-2.1-0-x86_64.pkg.tar.zst")
	// create the directories in the cache
	require.NoError(t, os.Mkdir(path.Join(config.CacheDir, "pkgs", "repo3"), os.ModePerm))

	defer os.RemoveAll(path.Join(config.CacheDir, "pkgs"))
	// create this file in the cache
	pkgAtCache := path.Join(config.CacheDir, "pkgs", "repo3", "acl-2.1-0-x86_64.pkg.tar.zst")
	pkgAtCacheContent := "cached old content"

	require.NoError(t, os.WriteFile(pkgAtCache, []byte(pkgAtCacheContent), os.ModePerm))
	require.NoError(t, os.WriteFile(pkgAtCache+".sig", []byte(pkgAtCacheContent), os.ModePerm))

	// create a package file which should be prefetched with signature (the signature is invalid but I'm not checking it) on the mirror
	pkgAtMirror := path.Join(mirrorDir, "mirror3", "acl-2.3.1-1-x86_64.pkg.tar.zst")
	pkgContent := "TEST content for the file to be prefetched"
	require.NoError(t, os.WriteFile(pkgAtMirror, []byte(pkgContent), os.ModePerm))
	require.NoError(t, os.WriteFile(pkgAtMirror+".sig", []byte(pkgContent), os.ModePerm))
	// create an updated package file with a wrong extension which should NOT be prefetched with signature (the signature is invalid but I'm not checking it) on the mirror
	wrongPkgAtMirror := path.Join(mirrorDir, "mirror3", "acl-2.3.1-1-x86_64.pkg.tar")
	wrongPkgContent := "TEST content for the file which should NOT be prefetched"
	require.NoError(t, os.WriteFile(wrongPkgAtMirror, []byte(wrongPkgContent), os.ModePerm))
	require.NoError(t, os.WriteFile(wrongPkgAtMirror+".sig", []byte(wrongPkgContent), os.ModePerm))
	// now if we call the prefetch procedure, it should try to prefetch the file and its signature
	prefetchPackages()
	// old packages should have been deleted
	exists, err := fileExists(pkgAtCache)
	require.NoError(t, err)
	require.Falsef(t, exists, "The file %v should not exist", pkgAtCache)
	exists, err = fileExists(pkgAtCache + ".sig")
	require.NoError(t, err)
	require.Falsef(t, exists, "The file %v should not exist", pkgAtCache+".sig")
	// new packages should appear
	pkgAtCache = path.Join(config.CacheDir, "pkgs", "repo3", "acl-2.3.1-1-x86_64.pkg.tar.zst")
	exists, err = fileExists(pkgAtCache)
	require.NoError(t, err)
	require.Truef(t, exists, "The file %v should exist", pkgAtCache)
	exists, err = fileExists(pkgAtCache + ".sig")
	require.NoError(t, err)
	require.Truef(t, exists, "The file %v should exist", pkgAtCache+".sig")
	// Check if the content matches
	got, err := os.ReadFile(pkgAtCache)
	require.NoError(t, err)
	want := []byte(pkgContent)
	require.Equal(t, want, got)
	got, err = os.ReadFile(pkgAtCache + ".sig")
	require.NoError(t, err)
	want = []byte(pkgContent)
	require.Equal(t, want, got)
	// new packages with the wrong extension should not appear
	pkgAtCache = path.Join(config.CacheDir, "pkgs", "repo3", "acl-2.3.1-1-x86_64.pkg.tar")
	exists, err = fileExists(pkgAtCache)
	require.NoError(t, err)
	require.Falsef(t, exists, "The file %v should not exist", pkgAtCache)
	exists, err = fileExists(pkgAtCache + ".sig")
	require.NoError(t, err)
	require.Falsef(t, exists, "The file %v should not exist", pkgAtCache+".sig")
}
