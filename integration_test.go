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
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err)
	defer os.RemoveAll(mirrorDir)

	// For easier setup we are going to serve several Arch mirror trees by one
	// instance of http.FileServer
	mirror := httptest.NewServer(http.FileServer(http.Dir(mirrorDir)))
	defer mirror.Close()
	mirrorURL = mirror.URL

	// Now setup pacoloco cache dir
	testPacolocoDir, err = os.MkdirTemp(os.TempDir(), "*-pacoloco-repo")
	require.NoError(t, err)
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
	t.Run("testRequestDbMultipleTimes", testRequestDbMultipleTimes)
	t.Run("testRequestPackageFile", testRequestPackageFile)
	t.Run("testFailover", testFailover)

	_, err = os.Stat(path.Join(testPacolocoDir, DefaultDBName))
	require.NotErrorIs(t, err, os.ErrNotExist, "DB file should be created!")
}

func TestPacolocoIntegration(t *testing.T) {
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
	testPacolocoDir, err = os.MkdirTemp(os.TempDir(), "*-pacoloco-repo")
	require.NoError(t, err)
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
	t.Run("testRequestDbMultipleTimes", testRequestDbMultipleTimes)
	t.Run("testRequestPackageFile", testRequestPackageFile)
	t.Run("testFailover", testFailover)

	_, err = os.Stat(path.Join(testPacolocoDir, DefaultDBName))
	require.ErrorIs(t, err, os.ErrNotExist, "DB file shouldn't be created!")
}

func testInvalidURL(t *testing.T) {
	req := httptest.NewRequest("GET", "http://example.com/foo", nil)
	w := httptest.NewRecorder()
	pacolocoHandler(w, req)
	resp := w.Result()
	require.Equal(t, resp.StatusCode, 404)
}

func testRequestNonExistingDb(t *testing.T) {
	// Requesting non-existing repo
	req := httptest.NewRequest("GET", pacolocoURL+"/repo/test/test.db", nil)
	w := httptest.NewRecorder()
	pacolocoHandler(w, req)
	resp := w.Result()
	require.Equal(t, resp.StatusCode, 404)

	// check that no repo cached
	_, err := os.Stat(path.Join(testPacolocoDir, "pkgs", "test"))
	require.ErrorIs(t, err, os.ErrNotExist, "test repo should not cached")
}

func testRequestExistingRepo(t *testing.T) {
	// Requesting existing repo
	config.Repos["repo1"] = &Repo{}
	defer delete(config.Repos, "repo1")

	requestCounter, err := cacheRequestsCounter.GetMetricWithLabelValues("repo1")
	require.NoError(t, err)
	servedCounter, err := cacheServedCounter.GetMetricWithLabelValues("repo1")
	require.NoError(t, err)
	missedCounter, err := cacheMissedCounter.GetMetricWithLabelValues("repo1")
	require.NoError(t, err)
	cacheErrorCounter, err := cacheServingFailedCounter.GetMetricWithLabelValues("repo1")
	require.NoError(t, err)

	expectedRequests := testutil.ToFloat64(requestCounter) + 1
	expectedServed := testutil.ToFloat64(servedCounter)
	expectedMissed := testutil.ToFloat64(missedCounter)
	expectedErrorServed := testutil.ToFloat64(cacheErrorCounter) + 1 // expected since we 404

	req := httptest.NewRequest("GET", pacolocoURL+"/repo/repo1/test.db", nil)
	w := httptest.NewRecorder()
	pacolocoHandler(w, req)
	resp := w.Result()
	require.Equal(t, resp.StatusCode, 404)

	actualRequests := testutil.ToFloat64(requestCounter)
	actualServed := testutil.ToFloat64(servedCounter)
	actualMissed := testutil.ToFloat64(missedCounter)
	actualErrorServed := testutil.ToFloat64(cacheErrorCounter)

	require.Equal(t, expectedRequests, actualRequests)
	require.Equal(t, expectedServed, actualServed)
	require.Equal(t, expectedMissed, actualMissed)
	require.Equal(t, expectedErrorServed, actualErrorServed)

	// check that db is not cached
	_, err = os.Stat(path.Join(testPacolocoDir, "pkgs", "repo1", "test.db"))
	require.ErrorIs(t, err, os.ErrNotExist, "repo1/test.db should be cached")
}

func testRequestExistingRepoWithDb(t *testing.T) {
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
	require.NoError(t, os.Chtimes(dbAtMirror, dbModTime, dbModTime))

	requestCounter, err := cacheRequestsCounter.GetMetricWithLabelValues("repo2")
	require.NoError(t, err)
	servedCounter, err := cacheServedCounter.GetMetricWithLabelValues("repo2")
	require.NoError(t, err)
	missedCounter, err := cacheMissedCounter.GetMetricWithLabelValues("repo2")
	require.NoError(t, err)
	cacheErrorCounter, err := cacheServingFailedCounter.GetMetricWithLabelValues("repo2")
	require.NoError(t, err)

	expectedRequests := testutil.ToFloat64(requestCounter) + 1
	expectedServed := testutil.ToFloat64(servedCounter)
	expectedMissed := testutil.ToFloat64(missedCounter) + 1
	expectedErrorServed := testutil.ToFloat64(cacheErrorCounter)

	req := httptest.NewRequest("GET", pacolocoURL+"/repo/repo2/test.db", nil)
	w := httptest.NewRecorder()
	pacolocoHandler(w, req)
	resp := w.Result()
	require.Equal(t, resp.StatusCode, 200)
	content, err := io.ReadAll(w.Body)
	require.NoError(t, err)
	require.Equal(t, string(content), dbFileContent)
	require.Equal(t, resp.ContentLength, int64(len(dbFileContent)))
	expectedModTime := dbModTime.UTC().Format(http.TimeFormat)
	require.Equal(t, expectedModTime, w.Header().Get("Last-Modified"))

	// copying a file to server cache is operation that runs asynchronously to downloading from server
	// wait a bit until cache operations settle down
	time.Sleep(10 * time.Millisecond)

	actualRequests := testutil.ToFloat64(requestCounter)
	actualServed := testutil.ToFloat64(servedCounter)
	actualMissed := testutil.ToFloat64(missedCounter)
	actualErrorServed := testutil.ToFloat64(cacheErrorCounter)

	require.Equal(t, expectedRequests, actualRequests)
	require.Equal(t, expectedServed, actualServed)
	require.Equal(t, expectedMissed, actualMissed)
	require.Equal(t, expectedErrorServed, actualErrorServed)

	// check that repo is cached
	_, err = os.Stat(path.Join(testPacolocoDir, "pkgs", "repo2"))
	require.NotErrorIs(t, err, os.ErrNotExist, "repo2 repo should be cached")
	defer os.RemoveAll(path.Join(testPacolocoDir, "pkgs", "repo2"))
	content, err = os.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo2", "test.db"))
	require.NoError(t, err)
	require.Equal(t, string(content), dbFileContent)

	// Now let's modify the db content, pacoloco should refetch it
	dbFileContent = "This is a new content"
	require.NoError(t, os.WriteFile(dbAtMirror, []byte(dbFileContent), os.ModePerm))
	newDbModTime := time.Now()
	require.NoError(t, os.Chtimes(dbAtMirror, newDbModTime, newDbModTime))

	expectedRequests = testutil.ToFloat64(requestCounter) + 1
	expectedServed = testutil.ToFloat64(servedCounter)
	expectedMissed = testutil.ToFloat64(missedCounter) + 1
	expectedErrorServed = testutil.ToFloat64(cacheErrorCounter)

	req = httptest.NewRequest("GET", pacolocoURL+"/repo/repo2/test.db", nil)
	w = httptest.NewRecorder()
	pacolocoHandler(w, req)
	resp = w.Result()
	require.Equal(t, resp.StatusCode, 200)
	content, err = io.ReadAll(w.Body)
	require.NoError(t, err)
	require.Equal(t, string(content), dbFileContent)

	// copying a file to server cache is operation that runs asynchronously to downloading from server
	// wait a bit until cache operations settle down
	time.Sleep(10 * time.Millisecond)

	actualRequests = testutil.ToFloat64(requestCounter)
	actualServed = testutil.ToFloat64(servedCounter)
	actualMissed = testutil.ToFloat64(missedCounter)
	actualErrorServed = testutil.ToFloat64(cacheErrorCounter)

	require.Equal(t, expectedRequests, actualRequests)
	require.Equal(t, expectedServed, actualServed)
	require.Equal(t, expectedMissed, actualMissed)
	require.Equal(t, expectedErrorServed, actualErrorServed)
	// check that repo is cached
	_, err = os.Stat(path.Join(testPacolocoDir, "pkgs", "repo2"))
	require.NotErrorIs(t, err, os.ErrNotExist, "repo2 repo should be cached")
	content, err = os.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo2", "test.db"))
	require.NoError(t, err)
	require.Equal(t, string(content), dbFileContent)
	require.Equal(t, resp.ContentLength, int64(len(dbFileContent)))
	newExpectedModTime := newDbModTime.UTC().Format(http.TimeFormat)
	require.Equal(t, newExpectedModTime, w.Header().Get("Last-Modified"))
}

func testRequestPackageFile(t *testing.T) {
	// Requesting existing repo
	repo3 := &Repo{
		URL: mirrorURL + "/mirror3",
	}
	config.Repos["repo3"] = repo3
	defer delete(config.Repos, "repo3")

	require.NoError(t, os.Mkdir(path.Join(mirrorDir, "mirror3"), os.ModePerm))
	defer os.RemoveAll(path.Join(mirrorDir, "mirror3"))

	pkgAtMirror := path.Join(mirrorDir, "mirror3", "test-1-any.pkg.tar.zst")
	pkgFileContent := "a package"
	require.NoError(t, os.WriteFile(pkgAtMirror, []byte(pkgFileContent), os.ModePerm))
	// Make the mirror file old enough to distinguish it from the subsequent modifications
	pkgModTime := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(pkgAtMirror, pkgModTime, pkgModTime))

	info, err := os.Stat(pkgAtMirror)
	require.NoError(t, err)
	requestCounter, err := cacheRequestsCounter.GetMetricWithLabelValues("repo3")
	require.NoError(t, err)
	servedCounter, err := cacheServedCounter.GetMetricWithLabelValues("repo3")
	require.NoError(t, err)
	missedCounter, err := cacheMissedCounter.GetMetricWithLabelValues("repo3")
	require.NoError(t, err)
	cacheErrorCounter, err := cacheServingFailedCounter.GetMetricWithLabelValues("repo3")
	require.NoError(t, err)
	cachePackageCounter, err := cachePackageGauge.GetMetricWithLabelValues("repo3")
	require.NoError(t, err)
	cachePackageSize, err := cacheSizeGauge.GetMetricWithLabelValues("repo3")
	require.NoError(t, err)

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

	require.Equal(t, resp.StatusCode, 200)
	content, err := io.ReadAll(w.Body)
	require.NoError(t, err)
	require.Equal(t, string(content), pkgFileContent)
	require.Equal(t, resp.ContentLength, int64(len(pkgFileContent)))
	expectedModTime := pkgModTime.UTC().Format(http.TimeFormat)
	require.Equal(t, expectedModTime, w.Header().Get("Last-Modified"))
	// copying a file to server cache is operation that runs asynchronously to downloading from server
	// wait a bit until cache operations settle down
	time.Sleep(10 * time.Millisecond)

	actualRequests := testutil.ToFloat64(requestCounter)
	actualServed := testutil.ToFloat64(servedCounter)
	actualMissed := testutil.ToFloat64(missedCounter)
	actualErrorServed := testutil.ToFloat64(cacheErrorCounter)
	actualPackageNum := testutil.ToFloat64(cachePackageCounter)
	actualSize := testutil.ToFloat64(cachePackageSize)

	require.Equal(t, expectedRequests, actualRequests)
	require.Equal(t, expectedServed, actualServed)
	require.Equal(t, expectedMissed, actualMissed)
	require.Equal(t, expectedErrorServed, actualErrorServed)
	require.Equal(t, expectedPackageNum, actualPackageNum)
	require.Equal(t, expectedSize, actualSize)

	// check that pkg is cached
	content, err = os.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo3", "test-1-any.pkg.tar.zst"))
	require.NoError(t, err)
	require.Equal(t, string(content), pkgFileContent)

	// Now let's modify the db content, pacoloco should not refetch it
	require.NoError(t, os.WriteFile(pkgAtMirror, []byte("This is a new content"), os.ModePerm))
	newDbModTime := time.Now()
	require.NoError(t, os.Chtimes(pkgAtMirror, newDbModTime, newDbModTime))

	expectedRequests = testutil.ToFloat64(requestCounter) + 1
	expectedServed = testutil.ToFloat64(servedCounter) + 1
	expectedMissed = testutil.ToFloat64(missedCounter)
	expectedErrorServed = testutil.ToFloat64(cacheErrorCounter)

	req = httptest.NewRequest("GET", pacolocoURL+"/repo/repo3/test-1-any.pkg.tar.zst", nil)
	w = httptest.NewRecorder()
	pacolocoHandler(w, req)
	resp = w.Result()
	require.Equal(t, resp.StatusCode, 200)
	content, err = io.ReadAll(w.Body)
	require.NoError(t, err)
	require.Equal(t, string(content), pkgFileContent)

	// check that repo is cached
	content, err = os.ReadFile(path.Join(testPacolocoDir, "pkgs", "repo3", "test-1-any.pkg.tar.zst"))
	require.NoError(t, err)
	require.Equal(t, string(content), pkgFileContent)
	require.Equal(t, resp.ContentLength, int64(len(pkgFileContent)))
	require.Equal(t, expectedModTime, w.Header().Get("Last-Modified"))
}

func testRequestDbMultipleTimes(t *testing.T) {
	// Requesting existing repo
	repo4 := &Repo{
		URL: mirrorURL + "/mirror4",
	}
	config.Repos["repo4"] = repo4
	defer delete(config.Repos, "repo4")

	require.NoError(t, os.Mkdir(path.Join(mirrorDir, "mirror4"), os.ModePerm))
	defer os.RemoveAll(path.Join(mirrorDir, "mirror4"))

	dbAtMirror := path.Join(mirrorDir, "mirror4", "test.db")
	dbFileContent := "pacoloco/mirror4.db"

	require.NoError(t, os.WriteFile(dbAtMirror, []byte(dbFileContent), os.ModePerm))

	req := httptest.NewRequest("GET", pacolocoURL+"/repo/repo4/test.db", nil)
	w := httptest.NewRecorder()
	pacolocoHandler(w, req)
	resp := w.Result()
	require.Equal(t, resp.StatusCode, 200)
	content, err := io.ReadAll(w.Body)
	require.NoError(t, err)
	require.Equal(t, string(content), dbFileContent)
	require.Equal(t, resp.ContentLength, int64(len(dbFileContent)))

	// check that repo is cached
	_, err = os.Stat(path.Join(testPacolocoDir, "pkgs", "repo4"))
	require.NotErrorIs(t, err, os.ErrNotExist, "repo4 repo should be cached")
	defer os.RemoveAll(path.Join(testPacolocoDir, "pkgs", "repo4"))

	req2 := httptest.NewRequest("GET", pacolocoURL+"/repo/repo4/test.db", nil)
	w2 := httptest.NewRecorder()
	pacolocoHandler(w2, req2)
	resp2 := w2.Result()
	require.Equal(t, resp2.StatusCode, 200)
	content2, err := io.ReadAll(w2.Body)
	require.NoError(t, err)
	require.Equal(t, string(content2), dbFileContent)
	require.Equal(t, resp2.ContentLength, int64(len(dbFileContent)))
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

	require.NoError(t, os.Mkdir(path.Join(mirrorDir, "mirror-failover"), os.ModePerm))
	defer os.RemoveAll(path.Join(mirrorDir, "mirror-failover"))

	pkgAtMirror := path.Join(mirrorDir, "mirror-failover", "test-1-any.pkg.tar.zst")
	pkgFileContent := "failover content"
	require.NoError(t, os.WriteFile(pkgAtMirror, []byte(pkgFileContent), os.ModePerm))

	requestCounter, err := cacheRequestsCounter.GetMetricWithLabelValues("failover")
	require.NoError(t, err)
	servedCounter, err := cacheServedCounter.GetMetricWithLabelValues("failover")
	require.NoError(t, err)
	missedCounter, err := cacheMissedCounter.GetMetricWithLabelValues("failover")
	require.NoError(t, err)
	cacheErrorCounter, err := cacheServingFailedCounter.GetMetricWithLabelValues("failover")
	require.NoError(t, err)

	expectedRequests := testutil.ToFloat64(requestCounter) + 1
	expectedServed := testutil.ToFloat64(servedCounter)
	expectedMissed := testutil.ToFloat64(missedCounter) + 1
	expectedErrorServed := testutil.ToFloat64(cacheErrorCounter)

	req := httptest.NewRequest("GET", pacolocoURL+"/repo/failover/test-1-any.pkg.tar.zst", nil)
	w := httptest.NewRecorder()
	pacolocoHandler(w, req)
	resp := w.Result()

	defer os.RemoveAll(path.Join(testPacolocoDir, "pkgs", "failover")) // remove cached content

	require.Equal(t, resp.StatusCode, 200)
	content, err := io.ReadAll(w.Body)
	require.NoError(t, err)
	require.Equal(t, string(content), pkgFileContent)
	require.Equal(t, resp.ContentLength, int64(len(pkgFileContent)))

	actualRequests := testutil.ToFloat64(requestCounter)
	actualServed := testutil.ToFloat64(servedCounter)
	actualMissed := testutil.ToFloat64(missedCounter)
	actualErrorServed := testutil.ToFloat64(cacheErrorCounter)

	require.Equal(t, expectedRequests, actualRequests)
	require.Equal(t, expectedServed, actualServed)
	require.Equal(t, expectedMissed, actualMissed)
	require.Equal(t, expectedErrorServed, actualErrorServed)
}
