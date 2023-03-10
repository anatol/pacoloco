package main

import (
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
	mirrorDir, err = os.MkdirTemp(os.TempDir(), "*-pacoloco-mirror")
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
	config.Repos["repo1"] = &Repo{}
	config.Repos["repo1"].URL = "example.com/foo"
	defer delete(config.Repos, "repo1")
	if err := prefetchRequest("/repo/repo1/test.db", ""); err == nil {
		t.Error("Error expected")
	}

	// check that db is not cached
	if _, err := os.Stat(path.Join(testPacolocoDir, "pkgs", "repo1", "test.db")); !os.IsNotExist(err) {
		t.Error("repo1/test.db should not be cached")
	}
	config.Repos["repo2"] = &Repo{}
	config.Repos["repo2"].URLs = []string{"example.com/foo", "example.com/bar"}
	if err := prefetchRequest("/repo/repo2/test2.db", ""); err == nil {
		t.Error("Error expected")
	}

	// check that db is not cached
	if _, err := os.Stat(path.Join(testPacolocoDir, "pkgs", "repo2", "test2.db")); !os.IsNotExist(err) {
		t.Error("repo1/test.db should not be cached")
	}
	defer delete(config.Repos, "repo2")
}

func testPrefetchRequestPackageFile(t *testing.T) {
	// Requesting existing repo
	repo5 := &Repo{
		URL: mirrorURL + "/mirror5",
	}
	config.Repos["repo5"] = repo5
	defer delete(config.Repos, "repo5")

	if err := os.Mkdir(path.Join(mirrorDir, "mirror5"), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(path.Join(mirrorDir, "mirror5"))

	pkgAtMirror := path.Join(mirrorDir, "mirror5", "test-1-1-any.pkg.tar.zst")
	pkgFileContent := "a package"
	if err := os.WriteFile(pkgAtMirror, []byte(pkgFileContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	// Make the mirror file old enough to distinguish it from the subsequent modifications
	pkgModTime := time.Now().Add(-time.Hour)
	os.Chtimes(pkgAtMirror, pkgModTime, pkgModTime)

	err := prefetchRequest("/repo/repo5/test-1-1-any.pkg.tar.zst", "")

	defer os.RemoveAll(path.Join(testPacolocoDir, "pkgs", "repo5")) // remove cached content

	if err != nil {
		t.Errorf("Expected success, got %v", err)
	}
	content, err := os.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo5", "test-1-1-any.pkg.tar.zst"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != pkgFileContent {
		t.Errorf("Pacoloco cached incorrect pkg content: %v", string(content))
	}

	// Now let's modify the db content, pacoloco should refetch it, cause prefetch should force update packages
	// This can also be useful to redownload packages with a wrong signature
	newContent := "This is a new content"
	if err := os.WriteFile(pkgAtMirror, []byte(newContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	newDbModTime := time.Now()
	os.Chtimes(pkgAtMirror, newDbModTime, newDbModTime)

	err = prefetchRequest("/repo/repo5/test-1-1-any.pkg.tar.zst", "") // the same file still exists, there is no need to download it again

	if err == nil {
		t.Errorf("Expected failure, got %v", err)
	}

	content, err = os.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo5", "test-1-1-any.pkg.tar.zst"))
	if err != nil {
		log.Fatal(err)
	}
	if string(content) != pkgFileContent {
		t.Errorf("Pacoloco cached incorrect db content: %v, expected %v. The new content had been set to %v.", string(content), string(pkgFileContent), string(newContent))
	}
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

	if err := os.Mkdir(path.Join(mirrorDir, "mirror-failover"), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(path.Join(mirrorDir, "mirror-failover"))

	pkgAtMirror := path.Join(mirrorDir, "mirror-failover", "test-1-1-any.pkg.tar.zst")
	pkgFileContent := "failover content"
	if err := os.WriteFile(pkgAtMirror, []byte(pkgFileContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}

	err := prefetchRequest("/repo/failover/test-1-1-any.pkg.tar.zst", "")

	defer os.RemoveAll(path.Join(testPacolocoDir, "pkgs", "failover")) // remove cached content

	if err != nil {
		t.Errorf("Expected success, got %v", err)
	}
	content, err := os.ReadFile(path.Join(testPacolocoDir, "pkgs", "failover", "test-1-1-any.pkg.tar.zst"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != pkgFileContent {
		t.Errorf("Pacoloco cached incorrect pkg content: %v", string(content))
	}
}

// prefetch an actual db and parses it
func testPrefetchRealDB(t *testing.T) {
	repo2 := &Repo{
		URL: mirrorURL + "/mirror2",
	}
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

	repo2 := &Repo{
		URL: mirrorURL + "/mirror2",
	}
	config.Repos["repo2"] = repo2
	defer delete(config.Repos, "repo2")

	if err := os.Mkdir(path.Join(mirrorDir, "mirror2"), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(path.Join(mirrorDir, "mirror2"))

	dbAtMirror := path.Join(mirrorDir, "mirror2", "test.db")
	dbFileContent := "pacoloco/mirror2.db"

	if err := os.WriteFile(dbAtMirror, []byte(dbFileContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	// Make the mirror file old enough to distinguish it from the subsequent modifications
	dbModTime := time.Now().Add(-time.Hour)
	os.Chtimes(dbAtMirror, dbModTime, dbModTime)

	err := prefetchRequest("/repo/repo2/test.db", "")
	if err != nil {
		t.Errorf("Expected success, got %v", err)
	}

	content, err := os.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo2", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != dbFileContent {
		t.Errorf("Pacoloco cached incorrect db content: %v", string(content))
	}

	defer os.RemoveAll(path.Join(testPacolocoDir, "pkgs", "repo2"))
	content, err = os.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo2", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != dbFileContent {
		t.Errorf("Got incorrect db content: %v", string(content))
	}

	// The prefetching mechanism should NOT refetch the file, as it is its job to provide cached content
	oldContent := dbFileContent
	dbFileContent = "This is a new content"
	if err := os.WriteFile(dbAtMirror, []byte(dbFileContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	newDbModTime := time.Now()
	os.Chtimes(dbAtMirror, newDbModTime, newDbModTime)

	prefetchRequest("/repo/repo2/test.db", "")
	if err != nil {
		t.Errorf("Expected success, got %v", err)
	}

	content, err = os.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo2", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != oldContent {
		t.Errorf("Pacoloco cached unexpected db content: %v\n instead of %v. The new content was %v", string(content), string(oldContent), string(dbFileContent))
	}
}

// The most complete integration test for prefetching
func testIntegrationPrefetchAllPkgs(t *testing.T) {
	// Setting up an existing repository
	testSetupHelper(t)
	repo6 := &Repo{}
	repo6.URL = mirrorURL + "/mirror6"
	config.Repos["repo6"] = repo6
	defer delete(config.Repos, "repo6")
	// create a mirror dir, so that on mirror6 you can fetch the db
	if err := os.Mkdir(path.Join(mirrorDir, "mirror6"), os.ModePerm); err != nil {
		t.Errorf("Can't create mirror6 directory, %v", err)
	}
	defer os.RemoveAll(path.Join(mirrorDir, "mirror6"))
	// create a valid db file with acl entry for version 2.3.1-1 on the mirror, to be served on mirror6 path
	dbAtMirror := path.Join(mirrorDir, "mirror6", "test.db")
	createDbTarball(dbAtMirror, getTestTarDB())
	// fake a request to the db, so that there is a record of a requested test.db file
	if _, err := updateDBRequestedDB("repo6", "", "/test.db"); err != nil {
		t.Errorf("Should not generate errors, but got %v", err)
	}
	// assume an older version of acl package had been successfully requested (this should create a first entry in the db)
	updateDBRequestedFile("repo6", "acl-2.0-0-x86_64.pkg.tar.zst")
	// assume that a newer version had been successfully requested (this should update the entry in the db)
	updateDBRequestedFile("repo6", "acl-2.1-0-x86_64.pkg.tar.zst")
	// create the directories in the pacoloco cache
	if err := os.MkdirAll(path.Join(config.CacheDir, "pkgs", "repo6"), os.ModePerm); err != nil {
		t.Fatal(err)
	}

	defer os.RemoveAll(path.Join(config.CacheDir, "pkgs"))
	// create the "latest" package file in the cache, to fake a previous successful fetching of the package
	pkgAtCache := path.Join(config.CacheDir, "pkgs", "repo6", "acl-2.1-0-x86_64.pkg.tar.zst")
	pkgAtCacheContent := "cached old content"

	if err := os.WriteFile(pkgAtCache, []byte(pkgAtCacheContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pkgAtCache+".sig", []byte(pkgAtCacheContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}

	// create an updated package file which should be prefetched with its signature (the signature is invalid but I'm not checking it) on the mirror
	pkgAtMirror := path.Join(mirrorDir, "mirror6", "acl-2.3.1-1-x86_64.pkg.tar.zst")
	pkgContent := "TEST content for the file to be prefetched"
	if err := os.WriteFile(pkgAtMirror, []byte(pkgContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pkgAtMirror+".sig", []byte(pkgContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	// create an updated package file with a wrong extension which should NOT be prefetched (with signature too) on the mirror
	wrongPkgAtMirror := path.Join(mirrorDir, "mirror6", "acl-2.3.1-1-x86_64.pkg.tar")
	wrongPkgContent := "TEST content for the file which should NOT be prefetched"
	if err := os.WriteFile(wrongPkgAtMirror, []byte(wrongPkgContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrongPkgAtMirror+".sig", []byte(wrongPkgContent), os.ModePerm); err != nil {
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
		t.Errorf("The file %v should not exist because a newer version should have been prefetched", pkgAtCache)
	}
	exists, err = fileExists(pkgAtCache + ".sig")
	if err != nil {
		log.Fatal(err)
	}
	if exists {
		t.Errorf("The file %v should not exist because a newer version should have been prefetched", pkgAtCache+".sig")
	}
	// new packages should appear
	pkgAtCache = path.Join(config.CacheDir, "pkgs", "repo6", "acl-2.3.1-1-x86_64.pkg.tar.zst")
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
	got, err := os.ReadFile(pkgAtCache)
	if err != nil {
		log.Fatal(err)
	}
	want := []byte(pkgContent)
	if !cmp.Equal(got, want) {
		t.Errorf("Got %v ,want %v", got, want)
	}
	got, err = os.ReadFile(pkgAtCache + ".sig")
	if err != nil {
		log.Fatal(err)
	}
	want = []byte(pkgContent)
	if !cmp.Equal(got, want) {
		t.Errorf("Got %v ,want %v", got, want)
	}
	// new packages with the wrong extension should not appear
	pkgAtCache = path.Join(config.CacheDir, "pkgs", "repo6", "acl-2.3.1-1-x86_64.pkg.tar")
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
