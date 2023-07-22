package main

import (
	"database/sql"
	"path"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/require"
)

func TestDeleteCreateMirrorPkgsTable(t *testing.T) {
	tmpDir := testSetupHelper(t)
	setupPrefetch()
	require.NoError(t, deleteMirrorPkgsTable())
	exists, err := fileExists(path.Join(tmpDir, DefaultDBName))
	require.NoError(t, err)
	require.Truef(t, exists, "setupPrefetch didn't create the db file")
	conn, err := sql.Open("sqlite3", path.Join(tmpDir, DefaultDBName))
	require.NoError(t, err)
	require.NotNil(t, conn)
	for _, table := range []string{"mirror_packages"} {
		_, err := conn.Query("select * from " + table)
		require.Errorf(t, err, "mirror_packages table shouldn't exist")
		require.NoError(t, createRepoTable())

		for _, table := range []string{"mirror_packages"} {
			_, err := conn.Query("select * from " + table)
			require.NoErrorf(t, err, "mirror_packages table should exist")
		}
		require.NoError(t, deleteMirrorPkgsTable())
		for _, table := range []string{"mirror_packages"} {
			_, err := conn.Query("select * from " + table)
			require.Errorf(t, err, "mirror_packages table shouldn't exist")
		}
	}
}

func TestCreatePrefetchDB(t *testing.T) {
	tmpDir := testSetupHelper(t)
	createPrefetchDB()
	exists, err := fileExists(path.Join(tmpDir, DefaultDBName))
	require.NoError(t, err)
	require.Truef(t, exists, "createPrefetchDB didn't create the db file")
	conn, err := sql.Open("sqlite3", path.Join(tmpDir, DefaultDBName))
	require.NoError(t, err)
	require.NotNil(t, conn)
	for _, table := range []string{"mirror_dbs", "packages", "mirror_packages"} {
		res, err := conn.Query("select * from " + table)
		require.NoError(t, err)
		for res.Next() {
			var pkg Package
			err = res.Scan(&pkg.PackageName, &pkg.Version, &pkg.Arch, &pkg.RepoName, &pkg.LastTimeDownloaded, &pkg.LastTimeRepoUpdated)
			require.NoError(t, err)
			require.Failf(t, "createPrefetchDB shouldn't create entries in %v\n", table)
		}
	}
}

func TestGetDBConnection(t *testing.T) {
	testSetupHelper(t)
	createPrefetchDB()
	conn, err := getDBConnection()
	require.NoError(t, err)
	require.NotNil(t, conn)
}

func TestGetPackage(t *testing.T) {
	now := time.Now()
	testSetupHelper(t)
	setupPrefetch()
	// I'm lazy, i use this to add data
	updateDBRequestedFile("foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	got := getPackage("webkit", "x86_64", "foo")
	want := Package{PackageName: "webkit", Version: "2.3.1-1", Arch: "x86_64", RepoName: "foo", LastTimeDownloaded: &now, LastTimeRepoUpdated: &now}
	require.True(t, cmp.Equal(got, want, cmpopts.IgnoreFields(Package{}, "LastTimeDownloaded", "LastTimeRepoUpdated")))
	// require.Equal(t, want, got)
	dist := want.LastTimeDownloaded.Sub(*got.LastTimeDownloaded)
	require.Greater(t, dist, -5*time.Second)
	require.Less(t, dist, 5*time.Second)
	dist = want.LastTimeRepoUpdated.Sub(*got.LastTimeRepoUpdated)
	require.Greater(t, dist, -5*time.Second)
	require.Less(t, dist, 5*time.Second)
}

func TestGetAndDropUnusedPackages(t *testing.T) {
	testSetupHelper(t)
	setupPrefetch()
	updateDBRequestedFile("foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	pkg := getPackage("webkit", "x86_64", "foo")
	require.NotEmpty(t, pkg.PackageName, "updateDBRequestedFile didn't work")
	oneMonthAgo := time.Now().AddDate(0, -1, 0) // more or less
	// updated one month ago but downloaded now, should not be deleted
	pkg.LastTimeRepoUpdated = &oneMonthAgo
	db := prefetchDB.Save(&pkg)
	require.NoError(t, db.Error)
	period := 24 * time.Hour * 10
	getAndDropUnusedPackages(period)
	// should delete nothing
	latestPkgInDB := getPackage("webkit", "x86_64", "foo")
	require.Truef(t, pkg.PackageName == latestPkgInDB.PackageName && pkg.Arch == latestPkgInDB.Arch, "Package shouldn't be altered")
	// now it should be deleted, knowing that a package is dead (in this configuration) if older than 20 days
	now := time.Now()
	pkg.LastTimeDownloaded = &oneMonthAgo
	pkg.LastTimeRepoUpdated = &now
	db = prefetchDB.Save(&pkg)
	require.NoError(t, db.Error)

	deletedPkgs := getAndDropUnusedPackages(period)
	require.NotEqualf(t, len(deletedPkgs), 0, "Package should have been deleted and returned")
	shouldNotExist := getPackage("webkit", "x86_64", "foo")
	require.Truef(t, shouldNotExist.PackageName == "" || shouldNotExist.Arch == "", "Package %v should have been deleted ", shouldNotExist)
}

func TestGetAndDropDeadPackages(t *testing.T) {
	testSetupHelper(t)
	setupPrefetch()
	updateDBRequestedFile("foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	pkg := getPackage("webkit", "x86_64", "foo")
	oneMonthAgo := time.Now().AddDate(0, -1, 0) // more or less
	// updated one month ago but downloaded now, should not be deleted
	pkg.LastTimeDownloaded = &oneMonthAgo
	db := prefetchDB.Save(&pkg)
	require.NoError(t, db.Error)
	getAndDropDeadPackages(oneMonthAgo)
	// should delete nothing
	latestPkgInDB := getPackage("webkit", "x86_64", "foo")
	require.Falsef(t, pkg.PackageName != latestPkgInDB.PackageName || pkg.Arch != latestPkgInDB.Arch, "Package shouldn't be altered, was \n%v, now it is \n%v", pkg, latestPkgInDB)
	// now it should be deleted, knowing that a package is dead (in this configuration) if older than 20 days
	pkg.LastTimeRepoUpdated = &oneMonthAgo
	db = prefetchDB.Save(&pkg)
	require.NoError(t, db.Error)

	getAndDropDeadPackages(oneMonthAgo.AddDate(0, 0, 1))
	latestPkgInDB = getPackage("webkit", "x86_64", "foo")
	require.False(t, latestPkgInDB.PackageName != "" && pkg.Arch != "", "Package should have been deleted")
}

func TestDropUnusedDBFiles(t *testing.T) {
	testSetupHelper(t)
	setupPrefetch()
	oneMonthAgo := time.Now().AddDate(0, -1, 0)
	// must be dropped because there is no repo called foo in testSetupHelper
	_, err := updateDBRequestedDB("foo", "/url/", "test2.db")
	require.NoError(t, err)
	// must not be dropped because there is a repo called example in testSetupHelper
	_, err = updateDBRequestedDB("example", "/url/", "test.db")
	require.NoError(t, err)
	dropUnusedDBFiles(oneMonthAgo)
	dbs := getAllMirrorsDB()
	require.Equal(t, len(dbs), 1)
	var mirr MirrorDB
	prefetchDB.Model(&MirrorDB{}).Where("mirror_dbs.url = ? and mirror_dbs.repo_name = ?", "/repo/example/url/test.db", "example").First(&mirr)
	matches := pathRegex.FindStringSubmatch(mirr.URL)
	require.NotEqualf(t, len(matches), 0, "It should be a proper pacoloco path url")
	twoMonthsAgo := time.Now().AddDate(0, -2, 0)
	mirr.LastTimeDownloaded = &twoMonthsAgo
	db := prefetchDB.Save(&mirr)
	require.NoError(t, db.Error)
	dropUnusedDBFiles(oneMonthAgo)
	dbs = getAllMirrorsDB()
	require.Equal(t, len(dbs), 0)
}

func TestGetPkgsToUpdate(t *testing.T) {
	// Create a repo pkg and a package, then check if it returns the couple
	testSetupHelper(t)
	setupPrefetch()
	updateDBRequestedFile("foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	updateDBRequestedFile("foo", "webkit2-2.3.1-1-x86_64.pkg.tar.zst")
	updateDBRequestedFile("foo", "webkit3-2.4.1-1-x86_64.pkg.tar.zst")
	repoPkg, err := buildMirrorPkg("webkit-2.4.1-1-x86_64.pkg.tar.zst", "foo", "")
	require.NoError(t, err)
	db := prefetchDB.Save(&repoPkg)
	require.NoError(t, db.Error)
	// same version, shouldn't be included
	repoPkg, err = buildMirrorPkg("webkit3-2.4.1-1-x86_64.pkg.tar.zst", "foo", "")
	require.NoError(t, err)
	db = prefetchDB.Save(&repoPkg)
	require.NoError(t, db.Error)
	got, err := getPkgsToUpdate()
	require.NoError(t, err)
	want := []PkgToUpdate{{PackageName: "webkit", RepoName: "foo", Arch: "x86_64", DownloadURL: "/repo/foo/webkit-2.4.1-1-x86_64", FileExt: ".pkg.tar.zst"}}
	require.Equal(t, want, got)
}

func TestGetPackageFromFilenameAndRepo(t *testing.T) {
	testSetupHelper(t)
	setupPrefetch()
	got, err := getPackageFromFilenameAndRepo("foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	want := Package{PackageName: "webkit", Version: "2.3.1-1", Arch: "x86_64", RepoName: "foo"}
	require.NoError(t, err)
	require.True(t, cmp.Equal(got, want, cmpopts.IgnoreFields(Package{}, "LastTimeDownloaded", "LastTimeRepoUpdated")))
	// require.Equal(t, want, got)
	_, err = getPackageFromFilenameAndRepo("foo", "webkit2-2.3\nhttp://www.example.org\n.1-1-x86_64.pkg.tar.zst")
	require.Error(t, err)
	_, err = getPackageFromFilenameAndRepo("foo", "android-sdk-26.1.1-1/1-x86_64.pkg.tar.xz")
	require.Error(t, err)
	got, err = getPackageFromFilenameAndRepo("t", "android-sdk-26.1.1-1.1-x86_64.pkg.tar.xz")
	want = Package{PackageName: "android-sdk", Version: "26.1.1-1.1", Arch: "x86_64", RepoName: "t"}
	require.NoError(t, err)
	require.True(t, cmp.Equal(got, want, cmpopts.IgnoreFields(Package{}, "LastTimeDownloaded", "LastTimeRepoUpdated")))
	// require.Equal(t, want, got)
}
