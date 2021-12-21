package main

import (
	"fmt"
	"path"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestDeleteCreateMirrorPkgsTable(t *testing.T) {
	tmpDir := testSetupHelper(t)
	setupPrefetch()
	if err := deleteMirrorPkgsTable(); err != nil {
		t.Fatal(err)
	}
	exists, err := fileExists(path.Join(tmpDir, DefaultDBName))
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("setupPrefetch didn't create the db file")
	}
	conn := testDbConnectionHelper(path.Join(tmpDir, DefaultDBName))
	for _, table := range []string{"mirror_packages"} {
		if _, err := conn.Query("select * from " + table); err == nil {
			t.Errorf("mirror_packages table shouldn't exist")
		}
	}
	if err := createRepoTable(); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{"mirror_packages"} {
		if _, err := conn.Query("select * from " + table); err != nil {
			t.Errorf("mirror_packages table should exist")
		}
	}
	if err := deleteMirrorPkgsTable(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"mirror_packages"} {
		if _, err := conn.Query("select * from " + table); err == nil {
			t.Errorf("mirror_packages table shouldn't exist")
		}
	}

}

func TestCreatePrefetchDB(t *testing.T) {
	tmpDir := testSetupHelper(t)
	createPrefetchDB()
	exists, err := fileExists(path.Join(tmpDir, DefaultDBName))
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("createPrefetchDB didn't create the db file")
	}
	conn := testDbConnectionHelper(path.Join(tmpDir, DefaultDBName))
	for _, table := range []string{"mirror_dbs", "packages", "mirror_packages"} {
		res, err := conn.Query("select * from " + table)
		if err != nil {
			t.Fatal(err)
		}
		for res.Next() {
			var pkg Package
			err = res.Scan(&pkg.PackageName, &pkg.Version, &pkg.Arch, &pkg.RepoName, &pkg.LastTimeDownloaded, &pkg.LastTimeRepoUpdated)
			fmt.Print(pkg)
			if err != nil {
				t.Fatal(err)
			}
			t.Fatalf("createPrefetchDB shouldn't create entries in %v\n", table)
		}
	}

}
func TestGetDBConnection(t *testing.T) {
	testSetupHelper(t)
	createPrefetchDB()
	conn, err := getDBConnection()
	if err != nil {
		t.Fatal(err)
	}
	if conn == nil {
		t.Error("getDBConnection shouldn't return nil")
	}
}
func TestGetPackage(t *testing.T) {
	now := time.Now()
	testSetupHelper(t)
	setupPrefetch()
	// I'm lazy, i use this to add data
	updateDBRequestedFile("foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	got := getPackage("webkit", "x86_64", "foo")
	want := Package{PackageName: "webkit", Version: "2.3.1-1", Arch: "x86_64", RepoName: "foo", LastTimeDownloaded: &now, LastTimeRepoUpdated: &now}
	if !cmp.Equal(got, want, cmpopts.IgnoreFields(Package{}, "LastTimeDownloaded", "LastTimeRepoUpdated")) {
		t.Errorf("\ngot  %v,\nwant %v", got, want)
	}
	dist := want.LastTimeDownloaded.Sub(*got.LastTimeDownloaded)
	if dist < -5*time.Second {
		t.Errorf("Unexpected result, got.LastTimeDownloaded is wrong, %d", dist)
	}
	if dist > 5*time.Second {
		t.Errorf("got %d, want %d", dist, 5*time.Second)
	}
	dist = want.LastTimeRepoUpdated.Sub(*got.LastTimeRepoUpdated)
	if dist < -5*time.Second {
		t.Errorf("Unexpected result, got.LastTimeRepoUpdated is wrong")
	}
	if want.LastTimeRepoUpdated.Sub(*got.LastTimeRepoUpdated) > 5*time.Second {
		t.Errorf("got %d, want %d", want.LastTimeRepoUpdated.Sub(*got.LastTimeRepoUpdated), 5*time.Second)
	}
}

func TestGetAndDropUnusedPackages(t *testing.T) {
	testSetupHelper(t)
	setupPrefetch()
	updateDBRequestedFile("foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	pkg := getPackage("webkit", "x86_64", "foo")
	if pkg.PackageName == "" {
		t.Error("updateDBRequestedFile didn't work")
	}
	oneMonthAgo := time.Now().AddDate(0, -1, 0) // more or less
	// updated one month ago but downloaded now, should not be deleted
	pkg.LastTimeRepoUpdated = &oneMonthAgo
	if db := prefetchDB.Save(&pkg); db.Error != nil {
		t.Error(db.Error)
	}
	period := time.Duration(24 * time.Hour * 10)
	getAndDropUnusedPackages(period)
	// should delete nothing
	latestPkgInDB := getPackage("webkit", "x86_64", "foo")
	if pkg.PackageName != latestPkgInDB.PackageName || pkg.Arch != latestPkgInDB.Arch {
		t.Errorf("Package shouldn't be altered, was \n%v, now it is \n%v", pkg, latestPkgInDB)
	}
	// now it should be deleted, knowing that a package is dead (in this configuration) if older than 20 days
	now := time.Now()
	pkg.LastTimeDownloaded = &oneMonthAgo
	pkg.LastTimeRepoUpdated = &now
	if db := prefetchDB.Save(&pkg); db.Error != nil {
		t.Error(db.Error)
	}
	deletedPkgs := getAndDropUnusedPackages(period)
	if len(deletedPkgs) == 0 {
		t.Errorf("Package should have been deleted and returned")
	}
	shouldNotExist := getPackage("webkit", "x86_64", "foo")
	if shouldNotExist.PackageName != "" && shouldNotExist.Arch != "" {
		t.Errorf("Package %v should have been deleted ", shouldNotExist)
	}
}

func TestGetAndDropDeadPackages(t *testing.T) {
	testSetupHelper(t)
	setupPrefetch()
	updateDBRequestedFile("foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	pkg := getPackage("webkit", "x86_64", "foo")
	oneMonthAgo := time.Now().AddDate(0, -1, 0) // more or less
	// updated one month ago but downloaded now, should not be deleted
	pkg.LastTimeDownloaded = &oneMonthAgo
	if db := prefetchDB.Save(pkg); db.Error != nil {
		t.Error(db.Error)
	}
	getAndDropDeadPackages(oneMonthAgo)
	// should delete nothing
	latestPkgInDB := getPackage("webkit", "x86_64", "foo")
	if pkg.PackageName != latestPkgInDB.PackageName || pkg.Arch != latestPkgInDB.Arch {
		t.Errorf("Package shouldn't be altered, was \n%v, now it is \n%v", pkg, latestPkgInDB)
	}
	// now it should be deleted, knowing that a package is dead (in this configuration) if older than 20 days
	pkg.LastTimeRepoUpdated = &oneMonthAgo
	if db := prefetchDB.Save(pkg); db.Error != nil {
		t.Error(db.Error)
	}
	getAndDropDeadPackages(oneMonthAgo.AddDate(0, 0, 1))
	latestPkgInDB = getPackage("webkit", "x86_64", "foo")
	if latestPkgInDB.PackageName != "" && pkg.Arch != "" {
		t.Errorf("Package should have been deleted")
	}

}

func TestDropUnusedDBFiles(t *testing.T) {
	testSetupHelper(t)
	setupPrefetch()
	oneMonthAgo := time.Now().AddDate(0, -1, 0)
	// must be dropped because there is no repo called foo in testSetupHelper
	if _, err := updateDBRequestedDB("foo", "/url/", "test2.db"); err != nil {
		t.Error(err)
	}
	// must not be dropped because there is a repo called example in testSetupHelper
	if _, err := updateDBRequestedDB("example", "/url/", "test.db"); err != nil {
		t.Error(err)
	}
	dropUnusedDBFiles(oneMonthAgo)
	dbs := getAllMirrorsDB()
	if len(dbs) != 1 {
		t.Errorf("The db should contain %d entries, but it contains %d", 1, len(dbs))
	}
	var mirr MirrorDB
	prefetchDB.Model(&MirrorDB{}).Where("mirror_dbs.url = ? and mirror_dbs.repo_name = ?", "/repo/example/url/test.db", "example").First(&mirr)
	matches := pathRegex.FindStringSubmatch(mirr.URL)
	if len(matches) == 0 {
		t.Errorf("It should be a proper pacoloco path url")
	}
	twoMonthsAgo := time.Now().AddDate(0, -2, 0)
	mirr.LastTimeDownloaded = &twoMonthsAgo
	if db := prefetchDB.Save(&mirr); db.Error != nil {
		t.Error(db.Error)
	}
	dropUnusedDBFiles(oneMonthAgo)
	dbs = getAllMirrorsDB()
	if len(dbs) != 0 {
		t.Errorf("The db should contain %d entries, but it contains %d", 0, len(dbs))
	}

}

func TestGetPkgsToUpdate(t *testing.T) {
	// Create a repo pkg and a package, then check if it returns the couple
	testSetupHelper(t)
	setupPrefetch()
	updateDBRequestedFile("foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	updateDBRequestedFile("foo", "webkit2-2.3.1-1-x86_64.pkg.tar.zst")
	updateDBRequestedFile("foo", "webkit3-2.4.1-1-x86_64.pkg.tar.zst")
	repoPkg, err := buildMirrorPkg("webkit-2.4.1-1-x86_64.pkg.tar.zst", "foo", "")
	if err != nil {
		t.Fatal(err)
	}
	if db := prefetchDB.Save(&repoPkg); db.Error != nil {
		t.Error(db.Error)
	}
	// same version, shouldn't be included
	repoPkg, err = buildMirrorPkg("webkit3-2.4.1-1-x86_64.pkg.tar.zst", "foo", "")
	if err != nil {
		t.Fatal(err)
	}
	if db := prefetchDB.Save(&repoPkg); db.Error != nil {
		t.Error(db.Error)
	}
	got := getPkgsToUpdate()
	want := []PkgToUpdate{PkgToUpdate{PackageName: "webkit", RepoName: "foo", Arch: "x86_64", DownloadURL: "/repo/foo/webkit-2.4.1-1-x86_64", FileExt: ".pkg.tar.zst"}}
	if !cmp.Equal(got, want) {
		t.Errorf("\ngot  %v\nwant %v", got, want)
	}
}

func TestGetPackageFromFilenameAndRepo(t *testing.T) {
	testSetupHelper(t)
	setupPrefetch()
	got, err := getPackageFromFilenameAndRepo("foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	want := Package{PackageName: "webkit", Version: "2.3.1-1", Arch: "x86_64", RepoName: "foo"}
	if err != nil {
		t.Fatal(err)
	}
	if !cmp.Equal(got, want, cmpopts.IgnoreFields(Package{}, "LastTimeDownloaded", "LastTimeRepoUpdated")) {
		t.Errorf("\ngot  %v\nwant %v", got, want)
	}
	_, err = getPackageFromFilenameAndRepo("foo", "webkit2-2.3\nhttp://www.example.org\n.1-1-x86_64.pkg.tar.zst")
	if err == nil {
		t.Fatal(err)
	}
	_, err = getPackageFromFilenameAndRepo("foo", "android-sdk-26.1.1-1/1-x86_64.pkg.tar.xz")
	if err == nil {
		t.Fatal(err)
	}
	got, err = getPackageFromFilenameAndRepo("t", "android-sdk-26.1.1-1.1-x86_64.pkg.tar.xz")
	want = Package{PackageName: "android-sdk", Version: "26.1.1-1.1", Arch: "x86_64", RepoName: "t"}
	if err != nil {
		t.Fatal(err)
	}
	if !cmp.Equal(got, want, cmpopts.IgnoreFields(Package{}, "LastTimeDownloaded", "LastTimeRepoUpdated")) {
		t.Errorf("\ngot  %v\nwant %v", got, want)
	}
}
