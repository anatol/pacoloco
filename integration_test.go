package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

var (
	mirrorURL       string
	pacolocoURL     string
	testPacolocoDir string
	mirrorDir       string
)

// the same as TestPacolocoIntegration, but with prefetching things
func TestPacolocoIntegrationWithPrefetching(t *testing.T) {
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
	testPacolocoDir, err = os.MkdirTemp(os.TempDir(), "*-pacoloco-repo")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(testPacolocoDir)
	notInvokingPrefetchTime := time.Now().Add(-time.Hour) // an hour ago
	config = &Config{
		CacheDir:        testPacolocoDir,
		Port:            -1,
		PurgeFilesAfter: -1,
		DownloadTimeout: 999,
		Repos:           make(map[string]*Repo),
		Prefetch:        &RefreshPeriod{Cron: "0 0 " + fmt.Sprint(notInvokingPrefetchTime.Hour()) + " ? * 1#1 *"},
	}
	setupPrefetch()
	pacoloco := httptest.NewServer(http.HandlerFunc(pacolocoHandler))
	defer pacoloco.Close()
	pacolocoURL = pacoloco.URL

	t.Run("testInvalidURL", testInvalidURL)
	t.Run("testRequestNonExistingDb", testRequestNonExistingDb)
	t.Run("testRequestExistingRepo", testRequestExistingRepo)
	t.Run("testRequestExistingRepoWithDb", testRequestExistingRepoWithDb)
	t.Run("testRequestPackageFile", testRequestPackageFile)
	t.Run("testFailover", testFailover)
	if _, err := os.Stat(path.Join(testPacolocoDir, DefaultDBName)); os.IsNotExist(err) {
		t.Errorf("DB file should be created!")
	}
}

func TestPacolocoIntegration(t *testing.T) {
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
	testPacolocoDir, err = os.MkdirTemp(os.TempDir(), "*-pacoloco-repo")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(testPacolocoDir)

	config = &Config{
		CacheDir:        testPacolocoDir,
		Port:            -1,
		PurgeFilesAfter: -1,
		DownloadTimeout: 999,
		Repos:           make(map[string]*Repo),
		Prefetch:        nil,
	}

	pacoloco := httptest.NewServer(http.HandlerFunc(pacolocoHandler))
	defer pacoloco.Close()
	pacolocoURL = pacoloco.URL

	t.Run("testInvalidURL", testInvalidURL)
	t.Run("testRequestNonExistingDb", testRequestNonExistingDb)
	t.Run("testRequestExistingRepo", testRequestExistingRepo)
	t.Run("testRequestExistingRepoWithDb", testRequestExistingRepoWithDb)
	t.Run("testRequestPackageFile", testRequestPackageFile)
	t.Run("testFailover", testFailover)
	if _, err := os.Stat(path.Join(testPacolocoDir, DefaultDBName)); !os.IsNotExist(err) {
		t.Errorf("DB file shouldn't be created!")
	}
}

func testInvalidURL(t *testing.T) {
	req := httptest.NewRequest("GET", "http://example.com/foo", nil)
	w := httptest.NewRecorder()
	pacolocoHandler(w, req)
	resp := w.Result()
	if resp.StatusCode != 404 {
		t.Error("404 response expected")
	}
}

func testRequestNonExistingDb(t *testing.T) {
	// Requesting non-existing repo
	req := httptest.NewRequest("GET", pacolocoURL+"/repo/test/test.db", nil)
	w := httptest.NewRecorder()
	pacolocoHandler(w, req)
	resp := w.Result()
	if resp.StatusCode != 404 {
		t.Error("404 response expected")
	}

	// check that no repo cached
	if _, err := os.Stat(path.Join(testPacolocoDir, "pkgs", "test")); !os.IsNotExist(err) {
		t.Error("test repo should not cached")
	}
}

func testRequestExistingRepo(t *testing.T) {
	// Requesting existing repo
	config.Repos["repo1"] = &Repo{}
	defer delete(config.Repos, "repo1")

	requestCounter, err := cacheRequestsCounter.GetMetricWithLabelValues("repo1")
	if err != nil {
		t.Error(err)
	}
	servedCounter, err := cacheServedCounter.GetMetricWithLabelValues("repo1")
	if err != nil {
		t.Error(err)
	}
	missedCounter, err := cacheMissedCounter.GetMetricWithLabelValues("repo1")
	if err != nil {
		t.Error(err)
	}
	cacheErrorCounter, err := cacheServingFailedCounter.GetMetricWithLabelValues("repo1")
	if err != nil {
		t.Error(err)
	}

	expectedRequests := testutil.ToFloat64(requestCounter) + 1
	expectedServed := testutil.ToFloat64(servedCounter)
	expectedMissed := testutil.ToFloat64(missedCounter)
	expectedErrorServed := testutil.ToFloat64(cacheErrorCounter) + 1 // expected since we 404

	req := httptest.NewRequest("GET", pacolocoURL+"/repo/repo1/test.db", nil)
	w := httptest.NewRecorder()
	pacolocoHandler(w, req)
	resp := w.Result()
	if resp.StatusCode != 404 {
		t.Error("404 response expected")
	}

	actualRequests := testutil.ToFloat64(requestCounter)
	actualServed := testutil.ToFloat64(servedCounter)
	actualMissed := testutil.ToFloat64(missedCounter)
	actualErrorServed := testutil.ToFloat64(cacheErrorCounter)

	if expectedRequests != actualRequests {
		t.Errorf("Request metric check failed: expected %v, got %v", expectedRequests, actualRequests)
	}
	if expectedServed != actualServed {
		t.Errorf("Served from cache metric check failed: expected %v, got %v", expectedServed, actualServed)
	}
	if expectedServed != actualServed {
		t.Errorf("Cache miss metric check failed: expected %v, got %v", expectedMissed, actualMissed)
	}
	if expectedErrorServed != actualErrorServed {
		t.Errorf("Cache error metric check failed: expected %v, got %v", expectedErrorServed, actualErrorServed)
	}

	// check that db is not cached
	if _, err := os.Stat(path.Join(testPacolocoDir, "pkgs", "repo1", "test.db")); !os.IsNotExist(err) {
		t.Error("repo1/test.db should be cached")
	}
}

func testRequestExistingRepoWithDb(t *testing.T) {
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
	if err := os.Chtimes(dbAtMirror, dbModTime, dbModTime); err != nil {
		t.Fatal(err)
	}

	requestCounter, err := cacheRequestsCounter.GetMetricWithLabelValues("repo2")
	if err != nil {
		t.Error(err)
	}
	servedCounter, err := cacheServedCounter.GetMetricWithLabelValues("repo2")
	if err != nil {
		t.Error(err)
	}
	missedCounter, err := cacheMissedCounter.GetMetricWithLabelValues("repo2")
	if err != nil {
		t.Error(err)
	}
	cacheErrorCounter, err := cacheServingFailedCounter.GetMetricWithLabelValues("repo2")
	if err != nil {
		t.Error(err)
	}

	expectedRequests := testutil.ToFloat64(requestCounter) + 1
	expectedServed := testutil.ToFloat64(servedCounter)
	expectedMissed := testutil.ToFloat64(missedCounter) + 1
	expectedErrorServed := testutil.ToFloat64(cacheErrorCounter)

	req := httptest.NewRequest("GET", pacolocoURL+"/repo/repo2/test.db", nil)
	w := httptest.NewRecorder()
	pacolocoHandler(w, req)
	resp := w.Result()
	if resp.StatusCode != 200 {
		t.Errorf("200 response expected, got %v", resp.StatusCode)
	}
	content, err := io.ReadAll(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != dbFileContent {
		t.Errorf("Pacoloco cached incorrect db content: %v", string(content))
	}
	if resp.ContentLength != int64(len(dbFileContent)) {
		t.Errorf("Pacoloco returns incorrect length %v", resp.ContentLength)
	}
	expectedModTime := dbModTime.UTC().Format(http.TimeFormat)
	if w.Header().Get("Last-Modified") != expectedModTime {
		t.Errorf("Incorrect Last-Modified received, expected: '%v' got: '%v'",
			expectedModTime,
			w.Header().Get("Last-Modified"))
	}

	actualRequests := testutil.ToFloat64(requestCounter)
	actualServed := testutil.ToFloat64(servedCounter)
	actualMissed := testutil.ToFloat64(missedCounter)
	actualErrorServed := testutil.ToFloat64(cacheErrorCounter)

	if expectedRequests != actualRequests {
		t.Errorf("Request metric check failed: expected %v, got %v", expectedRequests, actualRequests)
	}
	if expectedServed != actualServed {
		t.Errorf("Served from cache metric check failed: expected %v, got %v", expectedServed, actualServed)
	}
	if expectedMissed != actualMissed {
		t.Errorf("cache miss metric check failed: expected %v, got %v", expectedMissed, actualMissed)
	}
	if expectedErrorServed != actualErrorServed {
		t.Errorf("Cache error metric check failed: expected %v, got %v", expectedErrorServed, actualErrorServed)
	}

	// check that repo is cached
	if _, err = os.Stat(path.Join(testPacolocoDir, "pkgs", "repo2")); os.IsNotExist(err) {
		t.Error("repo2 repo should be cached")
	}
	defer os.RemoveAll(path.Join(testPacolocoDir, "pkgs", "repo2"))
	content, err = os.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo2", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != dbFileContent {
		t.Errorf("Got incorrect db content: %v", string(content))
	}

	// Now let's modify the db content, pacoloco should refetch it
	dbFileContent = "This is a new content"
	if err := os.WriteFile(dbAtMirror, []byte(dbFileContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	newDbModTime := time.Now()
	if err := os.Chtimes(dbAtMirror, newDbModTime, newDbModTime); err != nil {
		t.Fatal(err)
	}

	expectedRequests = testutil.ToFloat64(requestCounter) + 1
	expectedServed = testutil.ToFloat64(servedCounter)
	expectedMissed = testutil.ToFloat64(missedCounter) + 1
	expectedErrorServed = testutil.ToFloat64(cacheErrorCounter)

	req = httptest.NewRequest("GET", pacolocoURL+"/repo/repo2/test.db", nil)
	w = httptest.NewRecorder()
	pacolocoHandler(w, req)
	resp = w.Result()
	if resp.StatusCode != 200 {
		t.Errorf("200 response expected, got %v", resp.StatusCode)
	}
	content, err = io.ReadAll(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != dbFileContent {
		t.Errorf("Pacoloco cached incorrect db content: %v", string(content))
	}

	actualRequests = testutil.ToFloat64(requestCounter)
	actualServed = testutil.ToFloat64(servedCounter)
	actualMissed = testutil.ToFloat64(missedCounter)
	actualErrorServed = testutil.ToFloat64(cacheErrorCounter)

	if expectedRequests != actualRequests {
		t.Errorf("Request metric check failed: expected %v, got %v", expectedRequests, actualRequests)
	}
	if expectedServed != actualServed {
		t.Errorf("Served from cache metric check failed: expected %v, got %v", expectedServed, actualServed)
	}
	if expectedMissed != actualMissed {
		t.Errorf("cache miss metric check failed: expected %v, got %v", expectedMissed, actualMissed)
	}
	if expectedErrorServed != actualErrorServed {
		t.Errorf("Cache error metric check failed: expected %v, got %v", expectedErrorServed, actualErrorServed)
	}
	// check that repo is cached
	if _, err := os.Stat(path.Join(testPacolocoDir, "pkgs", "repo2")); os.IsNotExist(err) {
		t.Error("repo2 repo should be cached")
	}
	content, err = os.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo2", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != dbFileContent {
		t.Errorf("Got incorrect db content: %v", string(content))
	}
	if resp.ContentLength != int64(len(dbFileContent)) {
		t.Errorf("Pacoloco returns incorrect length %v", resp.ContentLength)
	}
	newExpectedModTime := newDbModTime.UTC().Format(http.TimeFormat)
	if w.Header().Get("Last-Modified") != newExpectedModTime {
		t.Errorf("Incorrect Last-Modified received, expected: '%v' got: '%v'",
			newExpectedModTime,
			w.Header().Get("Last-Modified"))
	}
}

func testRequestPackageFile(t *testing.T) {
	// Requesting existing repo
	repo3 := &Repo{
		URL: mirrorURL + "/mirror3",
	}
	config.Repos["repo3"] = repo3
	defer delete(config.Repos, "repo3")

	if err := os.Mkdir(path.Join(mirrorDir, "mirror3"), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(path.Join(mirrorDir, "mirror3"))

	pkgAtMirror := path.Join(mirrorDir, "mirror3", "test-1-any.pkg.tar.zst")
	pkgFileContent := "a package"
	if err := os.WriteFile(pkgAtMirror, []byte(pkgFileContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	// Make the mirror file old enough to distinguish it from the subsequent modifications
	pkgModTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(pkgAtMirror, pkgModTime, pkgModTime); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(pkgAtMirror)
	if err != nil {
		t.Error(err)
	}
	requestCounter, err := cacheRequestsCounter.GetMetricWithLabelValues("repo3")
	if err != nil {
		t.Error(err)
	}
	servedCounter, err := cacheServedCounter.GetMetricWithLabelValues("repo3")
	if err != nil {
		t.Error(err)
	}
	missedCounter, err := cacheMissedCounter.GetMetricWithLabelValues("repo3")
	if err != nil {
		t.Error(err)
	}
	cacheErrorCounter, err := cacheServingFailedCounter.GetMetricWithLabelValues("repo3")
	if err != nil {
		t.Error(err)
	}
	cachePackageCounter, err := cachePackageGauge.GetMetricWithLabelValues("repo3")
	if err != nil {
		t.Error(err)
	}
	cachePackageSize, err := cacheSizeGauge.GetMetricWithLabelValues("repo3")
	if err != nil {
		t.Error(err)
	}

	expectedRequests := testutil.ToFloat64(requestCounter) + 1
	expectedServed := testutil.ToFloat64(servedCounter)
	expectedMissed := testutil.ToFloat64(missedCounter) + 1
	expectedErrorServed := testutil.ToFloat64(cacheErrorCounter)
	expectedPackageNum := testutil.ToFloat64(cachePackageCounter) + 1
	expectedSize := testutil.ToFloat64(cachePackageSize) + float64(info.Size())

	req := httptest.NewRequest("GET", pacolocoURL+"/repo/repo3/test-1-any.pkg.tar.zst", nil)
	w := httptest.NewRecorder()
	pacolocoHandler(w, req)
	resp := w.Result()

	defer os.RemoveAll(path.Join(testPacolocoDir, "pkgs", "repo3")) // remove cached content

	if resp.StatusCode != 200 {
		t.Errorf("200 response expected, got %v", resp.StatusCode)
	}
	content, err := io.ReadAll(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != pkgFileContent {
		t.Errorf("Pacoloco cached incorrect pkg content: %v", string(content))
	}
	if resp.ContentLength != int64(len(pkgFileContent)) {
		t.Errorf("Pacoloco returns incorrect length %v", resp.ContentLength)
	}
	expectedModTime := pkgModTime.UTC().Format(http.TimeFormat)
	if w.Header().Get("Last-Modified") != expectedModTime {
		t.Errorf("Incorrect Last-Modified received, expected: '%v' got: '%v'",
			expectedModTime,
			w.Header().Get("Last-Modified"))
	}

	actualRequests := testutil.ToFloat64(requestCounter)
	actualServed := testutil.ToFloat64(servedCounter)
	actualMissed := testutil.ToFloat64(missedCounter)
	actualErrorServed := testutil.ToFloat64(cacheErrorCounter)
	actualPackageNum := testutil.ToFloat64(cachePackageCounter)
	actualSize := testutil.ToFloat64(cachePackageSize)

	if expectedRequests != actualRequests {
		t.Errorf("Request metric check failed: expected %v, got %v", expectedRequests, actualRequests)
	}
	if expectedServed != actualServed {
		t.Errorf("Served from cache metric check failed: expected %v, got %v", expectedServed, actualServed)
	}
	if expectedServed != actualServed {
		t.Errorf("Cache miss metric check failed: expected %v, got %v", expectedMissed, actualMissed)
	}
	if expectedErrorServed != actualErrorServed {
		t.Errorf("Cache error metric check failed: expected %v, got %v", expectedErrorServed, actualErrorServed)
	}
	if expectedPackageNum != actualPackageNum {
		t.Errorf("Cache package number metric check failed: expected %v, got %v", expectedPackageNum, actualPackageNum)
	}
	if expectedSize != actualSize {
		t.Errorf("Cache size metric check failed: expected %v, got %v", expectedSize, actualSize)
	}

	// check that pkg is cached
	content, err = os.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo3", "test-1-any.pkg.tar.zst"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != pkgFileContent {
		t.Errorf("Got incorrect db content: %v", string(content))
	}

	// Now let's modify the db content, pacoloco should not refetch it
	if err := os.WriteFile(pkgAtMirror, []byte("This is a new content"), os.ModePerm); err != nil {
		t.Fatal(err)
	}
	newDbModTime := time.Now()
	if err := os.Chtimes(pkgAtMirror, newDbModTime, newDbModTime); err != nil {
		t.Fatal(err)
	}

	expectedRequests = testutil.ToFloat64(requestCounter) + 1
	expectedServed = testutil.ToFloat64(servedCounter) + 1
	expectedMissed = testutil.ToFloat64(missedCounter)
	expectedErrorServed = testutil.ToFloat64(cacheErrorCounter)

	req = httptest.NewRequest("GET", pacolocoURL+"/repo/repo3/test-1-any.pkg.tar.zst", nil)
	w = httptest.NewRecorder()
	pacolocoHandler(w, req)
	resp = w.Result()
	if resp.StatusCode != 200 {
		t.Errorf("200 response expected, got %v", resp.StatusCode)
	}
	content, err = io.ReadAll(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != pkgFileContent {
		t.Errorf("Pacoloco cached incorrect db content: %v", string(content))
	}

	// check that repo is cached
	content, err = os.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo3", "test-1-any.pkg.tar.zst"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != pkgFileContent {
		t.Errorf("Got incorrect pkg content: %v", string(content))
	}
	if resp.ContentLength != int64(len(pkgFileContent)) {
		t.Errorf("Pacoloco returns incorrect length %v", resp.ContentLength)
	}
	if w.Header().Get("Last-Modified") != expectedModTime {
		t.Errorf("Incorrect Last-Modified received, expected: '%v' got: '%v'",
			expectedModTime,
			w.Header().Get("Last-Modified"))
	}
}

func testFailover(t *testing.T) {
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

	pkgAtMirror := path.Join(mirrorDir, "mirror-failover", "test-1-any.pkg.tar.zst")
	pkgFileContent := "failover content"
	if err := os.WriteFile(pkgAtMirror, []byte(pkgFileContent), os.ModePerm); err != nil {
		t.Fatal(err)
	}

	requestCounter, err := cacheRequestsCounter.GetMetricWithLabelValues("failover")
	if err != nil {
		t.Error(err)
	}
	servedCounter, err := cacheServedCounter.GetMetricWithLabelValues("failover")
	if err != nil {
		t.Error(err)
	}
	missedCounter, err := cacheMissedCounter.GetMetricWithLabelValues("failover")
	if err != nil {
		t.Error(err)
	}
	cacheErrorCounter, err := cacheServingFailedCounter.GetMetricWithLabelValues("failover")
	if err != nil {
		t.Error(err)
	}

	expectedRequests := testutil.ToFloat64(requestCounter) + 1
	expectedServed := testutil.ToFloat64(servedCounter)
	expectedMissed := testutil.ToFloat64(missedCounter) + 1
	expectedErrorServed := testutil.ToFloat64(cacheErrorCounter)

	req := httptest.NewRequest("GET", pacolocoURL+"/repo/failover/test-1-any.pkg.tar.zst", nil)
	w := httptest.NewRecorder()
	pacolocoHandler(w, req)
	resp := w.Result()

	defer os.RemoveAll(path.Join(testPacolocoDir, "pkgs", "failover")) // remove cached content

	if resp.StatusCode != 200 {
		t.Errorf("200 response expected, got %v", resp.StatusCode)
	}
	content, err := io.ReadAll(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != pkgFileContent {
		t.Errorf("Pacoloco cached incorrect pkg content: %v", string(content))
	}
	if resp.ContentLength != int64(len(pkgFileContent)) {
		t.Errorf("Pacoloco returns incorrect length %v", resp.ContentLength)
	}

	actualRequests := testutil.ToFloat64(requestCounter)
	actualServed := testutil.ToFloat64(servedCounter)
	actualMissed := testutil.ToFloat64(missedCounter)
	actualErrorServed := testutil.ToFloat64(cacheErrorCounter)

	if expectedRequests != actualRequests {
		t.Errorf("Request metric check failed: expected %v, got %v", expectedRequests, actualRequests)
	}
	if expectedServed != actualServed {
		t.Errorf("Served from cache metric check failed: expected %v, got %v", expectedServed, actualServed)
	}
	if expectedServed != actualServed {
		t.Errorf("Cache miss metric check failed: expected %v, got %v", expectedMissed, actualMissed)
	}
	if expectedErrorServed != actualErrorServed {
		t.Errorf("Cache error metric check failed: expected %v, got %v", expectedErrorServed, actualErrorServed)
	}
}
