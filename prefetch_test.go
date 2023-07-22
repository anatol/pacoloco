package main

import (
	"database/sql"
	"fmt"
	"os"
	"path"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/require"
)

// helper to setup the db
func testSetupHelper(t *testing.T) string {
	notInvokingPrefetchTime := time.Now().Add(-time.Hour) // an hour ago
	tmpDir := t.TempDir()
	c := `
cache_dir: ` + tmpDir + `
purge_files_after: 0 # 3600 * 24 * 30days
prefetch:
    cron: 0 0 ` + fmt.Sprint(notInvokingPrefetchTime.Hour()) + ` * * * *
    ttl_unaccessed_in_days: 15
    ttl_unupdated_in_days: 20
download_timeout: 200
port: 9139
repos:
    archlinux:
        url: http://mirrors.kernel.org/archlinux
    example:
        urls:
           -  http://mirror1.example.org/archlinux
           -  https://mirror.example.com/mirror/packages/archlinux/
           -  http://mirror2.example.com/archlinux/test/
`
	config = parseConfig([]byte(c))
	return tmpDir
}

func TestSetupPrefetch(t *testing.T) {
	tmpDir := testSetupHelper(t)
	setupPrefetch()
	exists, err := fileExists(path.Join(tmpDir, DefaultDBName))
	require.NoError(t, err)
	require.Truef(t, exists, "setupPrefetch didn't create the db file")
	require.NotNil(t, prefetchDB, "Prefetch DB is uninitilized")
	conn, err := sql.Open("sqlite3", path.Join(tmpDir, DefaultDBName))
	require.NoError(t, err)
	require.NotNil(t, conn)
	for _, table := range []string{"mirror_dbs", "packages", "mirror_packages"} {
		res, err := conn.Query("select * from " + table)
		require.NoError(t, err)
		for res.Next() {
			var pkg Package
			err = res.Scan(&pkg.PackageName, &pkg.Version, &pkg.Arch, &pkg.RepoName, &pkg.LastTimeDownloaded, &pkg.LastTimeRepoUpdated)
			fmt.Print(pkg)
			require.NoError(t, err)
			require.Failf(t, "setupPrefetch shouldn't create entries in %v\n", table)
		}
	}
}

func TestSetupPrefetchTicker(t *testing.T) {
	testSetupHelper(t)
	setupPrefetch()
	ticker := setupPrefetchTicker()
	require.NotNil(t, ticker)
	ticker.Stop()
}

func TestUpdateDBRequestedFile(t *testing.T) {
	tmpDir := testSetupHelper(t)
	setupPrefetch()
	conn, err := sql.Open("sqlite3", path.Join(tmpDir, DefaultDBName))
	require.NoError(t, err)
	require.NotNil(t, conn)
	updateDBRequestedFile("nope", "Wrongfile.db")
	updateDBRequestedFile("nope", "Wrongfile.zst")
	updateDBRequestedFile("nope", "fakeacceptablefile.pkg.tar.zst")     // doesn't have the correct format
	updateDBRequestedFile("nope", "acl-2.3.1-1-x86_64.pkg.tar.zst.sig") // do not save signatures too in the db
	// none of those should be in the db, now i'll check
	for _, table := range []string{"mirror_dbs", "packages", "mirror_packages"} {
		res, err := conn.Query("select * from " + table)
		require.NoError(t, err)
		for res.Next() {
			var pkg Package
			err = res.Scan(&pkg.PackageName, &pkg.Version, &pkg.Arch, &pkg.RepoName, &pkg.LastTimeDownloaded, &pkg.LastTimeRepoUpdated)
			fmt.Print(pkg)
			require.NoError(t, err)
			require.Failf(t, "updateDBRequestedFile shouldn't create entries in %v with bad values\n", table)
		}
	}
	// this one should be added
	updateDBRequestedFile("foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	res, err := conn.Query("select * from packages")
	require.NoError(t, err)
	if res.Next() {
		var got Package
		now := time.Now()
		err = res.Scan(&got.PackageName, &got.Version, &got.Arch, &got.RepoName, &got.LastTimeDownloaded, &got.LastTimeRepoUpdated)
		require.NoError(t, err)

		want := Package{PackageName: "webkit", Version: "2.3.1-1", Arch: "x86_64", RepoName: "foo", LastTimeDownloaded: &now, LastTimeRepoUpdated: &now}
		require.True(t, cmp.Equal(got, want, cmpopts.IgnoreFields(Package{}, "LastTimeDownloaded", "LastTimeRepoUpdated")))
		// require.Equal(t, want, got)
		dist := want.LastTimeDownloaded.Sub(*got.LastTimeDownloaded)
		require.Greater(t, dist, -5*time.Second)
		require.Less(t, dist, 5*time.Second)
		dist = want.LastTimeRepoUpdated.Sub(*got.LastTimeRepoUpdated)
		require.Greater(t, dist, -5*time.Second)
		require.Less(t, dist, 5*time.Second)
		return
	}
	require.Fail(t, "updateDBRequestedFile should create entries in packages\n")
}

func TestUpdateDBPrefetchedFile(t *testing.T) {
	tmpDir := testSetupHelper(t)
	setupPrefetch()
	conn, err := sql.Open("sqlite3", path.Join(tmpDir, DefaultDBName))
	require.NoError(t, err)
	require.NotNil(t, conn)
	// add a fake download entry
	updateDBRequestedFile("foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	oldPkgPath := path.Join(tmpDir, "pkgs", "foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")

	require.NoError(t, os.MkdirAll(path.Join(tmpDir, "pkgs"), 0o755))
	require.NoError(t, os.MkdirAll(path.Join(tmpDir, "pkgs", "foo"), 0o755))
	_, err = os.Create(oldPkgPath)
	require.NoError(t, err)
	_, err = os.Create(oldPkgPath + ".sig")
	require.NoError(t, err)

	// simulate a new prefetched file
	newPkgPath := path.Join(tmpDir, "pkgs", "foo", "webkit-2.5.10-4-x86_64.pkg.tar.zst")
	_, err = os.Create(newPkgPath)
	require.NoError(t, err)
	_, err = os.Create(newPkgPath + ".sig")
	require.NoError(t, err)
	updateDBPrefetchedFile("foo", "webkit-2.5.10-4-x86_64.pkg.tar.zst")
	// check if it properly exists
	res, err := conn.Query("select * from packages WHERE packages.package_name='webkit' AND packages.arch='x86_64' AND packages.repo_name='foo'")
	require.NoError(t, err)
	counter := 0
	for res.Next() {
		var got Package
		now := time.Now()
		err = res.Scan(&got.PackageName, &got.Version, &got.Arch, &got.RepoName, &got.LastTimeDownloaded, &got.LastTimeRepoUpdated)

		want := Package{PackageName: "webkit", Version: "2.5.10-4", Arch: "x86_64", RepoName: "foo", LastTimeDownloaded: &now, LastTimeRepoUpdated: &now}
		require.True(t, cmp.Equal(got, want, cmpopts.IgnoreFields(Package{}, "LastTimeDownloaded", "LastTimeRepoUpdated")))
		// require.Equal(t, want, got)
		dist := want.LastTimeDownloaded.Sub(*got.LastTimeDownloaded)
		require.Greater(t, dist, -5*time.Second)
		require.Less(t, dist, 5*time.Second)
		dist = want.LastTimeRepoUpdated.Sub(*got.LastTimeRepoUpdated)
		require.Greater(t, dist, -5*time.Second)
		require.Less(t, dist, 5*time.Second)
		require.NoError(t, err)
		counter++
	}
	require.Equal(t, 1, counter, "Too many entries")

	require.NotEqualf(t, counter, 0, "Too few entries, expected %d, found %d", 1, counter+1)
	// now, check if files have been properly handled
	exists, err := fileExists(oldPkgPath)
	require.NoError(t, err)
	require.Falsef(t, exists, "File %v should have been deleted", oldPkgPath)
	exists, err = fileExists(oldPkgPath + ".sig")
	require.NoError(t, err)
	require.Falsef(t, exists, "File %v should have been deleted", oldPkgPath+".sig")
	exists, err = fileExists(newPkgPath + ".sig")
	require.NoError(t, err)
	require.Truef(t, exists, "File %v should exist", newPkgPath+".sig")
	exists, err = fileExists(newPkgPath)
	require.NoError(t, err)
	require.Truef(t, exists, "File %v should exist", newPkgPath)
}

func TestPurgePkgIfExists(t *testing.T) {
	tmpDir := testSetupHelper(t)
	setupPrefetch()
	updateDBRequestedFile("foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	oldPkgPath := path.Join(tmpDir, "pkgs", "foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")

	require.NoError(t, os.MkdirAll(path.Join(tmpDir, "pkgs"), 0o755))

	require.NoError(t, os.MkdirAll(path.Join(tmpDir, "pkgs", "foo"), 0o755))
	_, err := os.Create(oldPkgPath)
	require.NoError(t, err)
	_, err = os.Create(oldPkgPath + ".sig")
	require.NoError(t, err)
	_, err = os.Create(oldPkgPath + ".ssig")
	require.NoError(t, err)
	pkgToPurge := getPackage("webkit", "x86_64", "foo")
	purgePkgIfExists(&pkgToPurge)
	// now, check if files have been properly handled
	exists, err := fileExists(oldPkgPath)
	require.NoError(t, err)
	require.Falsef(t, exists, "File %v should have been deleted", oldPkgPath)
	exists, err = fileExists(oldPkgPath + ".sig")
	require.NoError(t, err)
	require.Falsef(t, exists, "File %v should have been deleted", oldPkgPath+".sig")
	exists, err = fileExists(oldPkgPath + ".ssig")
	require.NoError(t, err)
	require.Truef(t, exists, "File %v should exist", oldPkgPath+".ssig")
}

func TestCleanPrefetchDB(t *testing.T) {
	tmpDir := testSetupHelper(t)
	setupPrefetch()
	updateDBRequestedFile("foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	oldPkgPath := path.Join(tmpDir, "pkgs", "foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")

	require.NoError(t, os.MkdirAll(path.Join(tmpDir, "pkgs"), 0o755))

	require.NoError(t, os.MkdirAll(path.Join(tmpDir, "pkgs", "foo"), 0o755))
	_, err := os.Create(oldPkgPath)
	require.NoError(t, err)
	_, err = os.Create(oldPkgPath + ".sig")
	require.NoError(t, err)
	// created some files
	cleanPrefetchDB()
	// should delete nothing
	exists, err := fileExists(oldPkgPath + ".sig")
	require.NoError(t, err)
	require.Truef(t, exists, "File %v should exist", oldPkgPath+".sig")
	exists, err = fileExists(oldPkgPath)
	require.NoError(t, err)
	require.Truef(t, exists, "File %v should exist", oldPkgPath)
	// now i update some of its data
	pkg := getPackage("webkit", "x86_64", "foo")
	oneMonthAgo := time.Now().AddDate(0, -1, 0) // more or less
	// updated one month ago but downloaded now, should not be deleted
	pkg.LastTimeRepoUpdated = &oneMonthAgo
	db := prefetchDB.Save(&pkg)
	require.NoError(t, db.Error)

	cleanPrefetchDB()
	// should delete nothing
	latestPkgInDB := getPackage("webkit", "x86_64", "foo")
	require.False(t, pkg.PackageName != latestPkgInDB.PackageName || pkg.Arch != latestPkgInDB.Arch || pkg.RepoName != latestPkgInDB.RepoName, "Package shouldn't be altered")
	exists, err = fileExists(oldPkgPath + ".sig")
	require.NoError(t, err)
	require.Truef(t, exists, "File %v should exist", oldPkgPath+".sig")
	exists, err = fileExists(oldPkgPath)
	require.NoError(t, err)
	require.Truef(t, exists, "File %v should exist", oldPkgPath)
	// now it should be deleted, knowing that a package is dead (in this configuration) if older than 20 days

	pkg.LastTimeDownloaded = &oneMonthAgo
	db = prefetchDB.Save(&pkg)
	require.NoError(t, db.Error)

	cleanPrefetchDB()
	latestPkgInDB = getPackage("webkit", "x86_64", "foo")
	require.False(t, latestPkgInDB.PackageName != "" && pkg.Arch != "", "Package should have been deleted")
	exists, err = fileExists(oldPkgPath + ".sig")
	require.NoError(t, err)
	require.Falsef(t, exists, "File %v should not exist", oldPkgPath+".sig")
	exists, err = fileExists(oldPkgPath)
	require.NoError(t, err)
	require.Falsef(t, exists, "File %v not should exist", oldPkgPath)
}

func TestPrefetchAllPkgs(t *testing.T) {
	// Fully tested in integration test
	testSetupHelper(t)
	setupPrefetch()
	cleanPrefetchDB()
	prefetchAllPkgs()
}

func TestPrefetchPackages(t *testing.T) {
	// Fully tested in integration test
	testSetupHelper(t)
	setupPrefetch()
	prefetchPackages()
}

func TestGetCronDuration(t *testing.T) {
	now := time.Now()
	var expectedTime time.Time
	if now.Hour() < 3 {
		expectedTime = now
	} else {
		expectedTime = now.AddDate(0, 0, 1)
	}
	expectedTime = time.Date(expectedTime.Year(), expectedTime.Month(), expectedTime.Day(), 3, 0, 0, 0, expectedTime.Location())
	expectedDuration := expectedTime.Sub(now)
	got, err := getCronDuration("0 0 3 * * * *", now)
	require.NoError(t, err)
	require.Equal(t, got, expectedDuration)
}
