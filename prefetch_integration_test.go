package main

import (
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestPacolocoPrefetchIntegration(t *testing.T) {
	var err error
	mirrorDir, err = ioutil.TempDir(os.TempDir(), "*-pacoloco-mirror")
	if err != nil {
		t.Fatal(err)
	}
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
	if err := prefetchRequest("/foo", ""); err == nil {
		t.Error("Error expected")
	}
}

func testPrefetchRequestNonExistingDb(t *testing.T) {
	// Requesting non-existing repo
	if err := prefetchRequest("/repo/test/test.db", ""); err == nil {
		t.Error("Error expected")
	}

	// check that no repo cached
	if _, err := os.Stat(path.Join(testPacolocoDir, "pkgs", "test")); !os.IsNotExist(err) {
		t.Error("test repo should not cached")
	}
}

func testPrefetchRequestExistingRepo(t *testing.T) {
	// Requesting existing repo
	config.Repos["repo1"] = makeTestRepo()
	defer delete(config.Repos, "repo1")

	if err := prefetchRequest("/repo/repo1/test.db", ""); err == nil {
		t.Error("Error expected")
	}

	// check that db is not cached
	if _, err := os.Stat(path.Join(testPacolocoDir, "pkgs", "repo1", "test.db")); !os.IsNotExist(err) {
		t.Error("repo1/test.db should not be cached")
	}
}

func testPrefetchRequestPackageFile(t *testing.T) {
	// Requesting existing repo
	repo3 := makeTestRepo()
	repo3.URL = mirrorURL + "/mirror3"
	config.Repos["repo3"] = repo3
	defer delete(config.Repos, "repo3")

	if err := os.Mkdir(path.Join(mirrorDir, "mirror3"), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(path.Join(mirrorDir, "mirror3"))

	pkgAtMirror := path.Join(mirrorDir, "mirror3", "test-1-1-any.pkg.tar.zst")
	pkgFileContent := "a package"
	if err := ioutil.WriteFile(pkgAtMirror, []byte(pkgFileContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	// Make the mirror file old enough to distinguish it from the subsequent modifications
	pkgModTime := time.Now().Add(-time.Hour)
	os.Chtimes(pkgAtMirror, pkgModTime, pkgModTime)

	err := prefetchRequest("/repo/repo3/test-1-1-any.pkg.tar.zst", "")

	defer os.RemoveAll(path.Join(testPacolocoDir, "pkgs", "repo3")) // remove cached content

	if err != nil {
		t.Errorf("Expected success, got %v", err)
	}
	content, err := ioutil.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo3", "test-1-1-any.pkg.tar.zst"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != pkgFileContent {
		t.Errorf("Pacoloco cached incorrect pkg content: %v", string(content))
	}

	// Now let's modify the db content, pacoloco should refetch it, cause prefetch should force update packages
	// This can also be useful to redownload packages with a wrong signature
	newContent := "This is a new content"
	if err := ioutil.WriteFile(pkgAtMirror, []byte(newContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	newDbModTime := time.Now()
	os.Chtimes(pkgAtMirror, newDbModTime, newDbModTime)

	err = prefetchRequest("/repo/repo3/test-1-1-any.pkg.tar.zst", "")

	if err != nil {
		t.Errorf("Expected success, got %v", err)
	}

	content, err = ioutil.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo3", "test-1-1-any.pkg.tar.zst"))
	if err != nil {
		log.Fatal(err)
	}
	if string(content) != newContent {
		t.Errorf("Pacoloco cached incorrect db content: %v", string(content))
	}
}

func testPrefetchFailover(t *testing.T) {
	failover := makeTestRepo()
	failover.URLs = []string{
		mirrorURL + "/no-mirror",
		mirrorURL + "/mirror-failover",
	}
	config.Repos["failover"] = failover
	defer delete(config.Repos, "failover")

	if err := os.Mkdir(path.Join(mirrorDir, "mirror-failover"), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(path.Join(mirrorDir, "mirror-failover"))

	pkgAtMirror := path.Join(mirrorDir, "mirror-failover", "test-1-1-any.pkg.tar.zst")
	pkgFileContent := "failover content"
	if err := ioutil.WriteFile(pkgAtMirror, []byte(pkgFileContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}

	err := prefetchRequest("/repo/failover/test-1-1-any.pkg.tar.zst", "")

	defer os.RemoveAll(path.Join(testPacolocoDir, "pkgs", "failover")) // remove cached content

	if err != nil {
		t.Errorf("Expected success, got %v", err)
	}
	content, err := ioutil.ReadFile(path.Join(testPacolocoDir, "pkgs", "failover", "test-1-1-any.pkg.tar.zst"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != pkgFileContent {
		t.Errorf("Pacoloco cached incorrect pkg content: %v", string(content))
	}
}

// prefetch an actual db and parses it
func testPrefetchRealDB(t *testing.T) {
	repo2 := makeTestRepo()
	repo2.URL = mirrorURL + "/mirror2"
	config.Repos["repo2"] = repo2
	defer delete(config.Repos, "repo2")

	if err := os.Mkdir(path.Join(mirrorDir, "mirror2"), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(path.Join(mirrorDir, "mirror2"))

	dbAtMirror := path.Join(mirrorDir, "mirror2", "test.db")
	createDbTarball(dbAtMirror, getTestTarDB())
	mirror, err := updateDBRequestedDB("repo2", "", "/test.db")
	if err != nil {
		t.Errorf("This shouldn't fail. Error: %v", err)
	}
	if err = downloadAndParseDb(mirror); err != nil {
		t.Errorf("This shouldn't fail at all. Error: %v", err)
	}
}

func testPrefetchRequestExistingRepoWithDb(t *testing.T) {
	// Requesting existing repo

	repo2 := makeTestRepo()
	repo2.URL = mirrorURL + "/mirror2"
	config.Repos["repo2"] = repo2
	defer delete(config.Repos, "repo2")

	if err := os.Mkdir(path.Join(mirrorDir, "mirror2"), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(path.Join(mirrorDir, "mirror2"))

	dbAtMirror := path.Join(mirrorDir, "mirror2", "test.db")
	dbFileContent := "pacoloco/mirror2.db"

	if err := ioutil.WriteFile(dbAtMirror, []byte(dbFileContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	// Make the mirror file old enough to distinguish it from the subsequent modifications
	dbModTime := time.Now().Add(-time.Hour)
	os.Chtimes(dbAtMirror, dbModTime, dbModTime)

	err := prefetchRequest("/repo/repo2/test.db", "")

	if err != nil {
		t.Errorf("Expected success, got %v", err)
	}

	content, err := ioutil.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo2", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != dbFileContent {
		t.Errorf("Pacoloco cached incorrect db content: %v", string(content))
	}

	defer os.RemoveAll(path.Join(testPacolocoDir, "pkgs", "repo2"))
	content, err = ioutil.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo2", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != dbFileContent {
		t.Errorf("Got incorrect db content: %v", string(content))
	}

	// Now let's modify the db content, pacoloco should refetch it
	dbFileContent = "This is a new content"
	if err := ioutil.WriteFile(dbAtMirror, []byte(dbFileContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	newDbModTime := time.Now()
	os.Chtimes(dbAtMirror, newDbModTime, newDbModTime)

	prefetchRequest("/repo/repo2/test.db", "")
	if err != nil {
		t.Errorf("Expected success, got %v", err)
	}

	content, err = ioutil.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo2", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != dbFileContent {
		t.Errorf("Pacoloco cached incorrect db content: %v\n instead of %v", string(content), string(dbFileContent))
	}
}

// The most complete integration test for prefetching
func testIntegrationPrefetchAllPkgs(t *testing.T) {
	// Setting up an existing repo
	repo3 := makeTestRepo()
	repo3.URL = mirrorURL + "/mirror3"
	config.Repos["repo3"] = repo3
	defer delete(config.Repos, "repo3")

	if err := os.Mkdir(path.Join(mirrorDir, "mirror3"), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(path.Join(mirrorDir, "mirror3"))
	// create a valid db file on the mirror
	dbAtMirror := path.Join(mirrorDir, "mirror3", "test.db")
	createDbTarball(dbAtMirror, getTestTarDB())
	// fake a request to the db
	if _, err := updateDBRequestedDB("repo3", "", "/test.db"); err != nil {
		t.Errorf("Should not generate errors, but got %v", err)
	}
	// now add a fake older version of a package which is in the db
	updateDBRequestedFile("repo3", "acl-2.0-0-x86_64.pkg.tar.zst")
	// now i add a bit newer one, to ensure that the db gets updated accordingly
	updateDBRequestedFile("repo3", "acl-2.1-0-x86_64.pkg.tar.zst")
	// create the directories in the cache
	if err := os.Mkdir(path.Join(config.CacheDir, "pkgs", "repo3"), os.ModePerm); err != nil {
		t.Fatal(err)
	}

	defer os.RemoveAll(path.Join(config.CacheDir, "pkgs"))
	// create this file in the cache
	pkgAtCache := path.Join(config.CacheDir, "pkgs", "repo3", "acl-2.1-0-x86_64.pkg.tar.zst")
	pkgAtCacheContent := "cached old content"

	if err := ioutil.WriteFile(pkgAtCache, []byte(pkgAtCacheContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(pkgAtCache+".sig", []byte(pkgAtCacheContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}

	// create a package file which should be prefetched with signature (the signature is invalid but I'm not checking it) on the mirror
	pkgAtMirror := path.Join(mirrorDir, "mirror3", "acl-2.3.1-1-x86_64.pkg.tar.zst")
	pkgContent := "TEST content for the file to be prefetched"
	if err := ioutil.WriteFile(pkgAtMirror, []byte(pkgContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(pkgAtMirror+".sig", []byte(pkgContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	// create an updated package file with a wrong extension which should NOT be prefetched with signature (the signature is invalid but I'm not checking it) on the mirror
	wrongPkgAtMirror := path.Join(mirrorDir, "mirror3", "acl-2.3.1-1-x86_64.pkg.tar")
	wrongPkgContent := "TEST content for the file which should NOT be prefetched"
	if err := ioutil.WriteFile(wrongPkgAtMirror, []byte(wrongPkgContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(wrongPkgAtMirror+".sig", []byte(wrongPkgContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	// now if we call the prefetch procedure, it should try to prefetch the file and its signature
	prefetchPackages()
	// old packages should have been deleted
	exists, err := fileExists(pkgAtCache)
	if err != nil {
		log.Fatal(err)
	}
	if exists {
		t.Errorf("The file %v should not exist", pkgAtCache)
	}
	exists, err = fileExists(pkgAtCache + ".sig")
	if err != nil {
		log.Fatal(err)
	}
	if exists {
		t.Errorf("The file %v should not exist", pkgAtCache+".sig")
	}
	// new packages should appear
	pkgAtCache = path.Join(config.CacheDir, "pkgs", "repo3", "acl-2.3.1-1-x86_64.pkg.tar.zst")
	exists, err = fileExists(pkgAtCache)
	if err != nil {
		log.Fatal(err)
	}
	if !exists {
		t.Errorf("The file %v should exist", pkgAtCache)
	}
	exists, err = fileExists(pkgAtCache + ".sig")
	if err != nil {
		log.Fatal(err)
	}
	if !exists {
		t.Errorf("The file %v should exist", pkgAtCache+".sig")
	}
	// Check if the content matches
	got, err := ioutil.ReadFile(pkgAtCache)
	if err != nil {
		log.Fatal(err)
	}
	want := []byte(pkgContent)
	if !cmp.Equal(got, want) {
		t.Errorf("Got %v ,want %v", got, want)
	}
	got, err = ioutil.ReadFile(pkgAtCache + ".sig")
	if err != nil {
		log.Fatal(err)
	}
	want = []byte(pkgContent)
	if !cmp.Equal(got, want) {
		t.Errorf("Got %v ,want %v", got, want)
	}
	// new packages with the wrong extension should not appear
	pkgAtCache = path.Join(config.CacheDir, "pkgs", "repo3", "acl-2.3.1-1-x86_64.pkg.tar")
	exists, err = fileExists(pkgAtCache)
	if err != nil {
		log.Fatal(err)
	}
	if exists {
		t.Errorf("The file %v should not exist", pkgAtCache)
	}
	exists, err = fileExists(pkgAtCache + ".sig")
	if err != nil {
		log.Fatal(err)
	}
	if exists {
		t.Errorf("The file %v should not exist", pkgAtCache+".sig")
	}
}
