package main

import (
	"fmt"
	"path"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestDeleteCreateRepoTable(t *testing.T) {
	tmpDir := testSetupHelper(t)
	setupPrefetch()
	if err := deleteRepoTable(); err != nil {
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
	for _, table := range []string{"repo_packages"} {
		if _, err := conn.Query("select * from " + table); err == nil {
			t.Errorf("repo_packages table shouldn't exist")
		}
	}
	if err := createRepoTable(); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{"repo_packages"} {
		if _, err := conn.Query("select * from " + table); err != nil {
			t.Errorf("repo_packages table should exist")
		}
	}
	if err := deleteRepoTable(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"repo_packages"} {
		if _, err := conn.Query("select * from " + table); err == nil {
			t.Errorf("repo_packages table shouldn't exist")
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
	for _, table := range []string{"mirror_dbs", "packages", "repo_packages"} {
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
	updateDBDownloadedFile("foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
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
	updateDBDownloadedFile("foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	pkg := getPackage("webkit", "x86_64", "foo")
	if pkg.PackageName == "" {
		t.Error("updateDBDownloadedFile didn't work")
	}
	oneMonthAgo := time.Now().AddDate(0, -1, 0) // more or less
	// updated one month ago but downloaded now, should not be deleted
	pkg.LastTimeRepoUpdated = &oneMonthAgo
	prefetchDB.Save(&pkg)
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
	prefetchDB.Save(&pkg)
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
	updateDBDownloadedFile("foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	pkg := getPackage("webkit", "x86_64", "foo")
	oneMonthAgo := time.Now().AddDate(0, -1, 0) // more or less
	// updated one month ago but downloaded now, should not be deleted
	pkg.LastTimeDownloaded = &oneMonthAgo
	prefetchDB.Save(pkg)
	getAndDropDeadPackages(oneMonthAgo)
	// should delete nothing
	latestPkgInDB := getPackage("webkit", "x86_64", "foo")
	if pkg.PackageName != latestPkgInDB.PackageName || pkg.Arch != latestPkgInDB.Arch {
		t.Errorf("Package shouldn't be altered, was \n%v, now it is \n%v", pkg, latestPkgInDB)
	}
	// now it should be deleted, knowing that a package is dead (in this configuration) if older than 20 days
	pkg.LastTimeRepoUpdated = &oneMonthAgo
	prefetchDB.Save(pkg)
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
	if _, err := addDBfileToDB("test.db", "foo"); err == nil {
		t.Errorf("Should have raised an error cause url is invalid")
	}
	if _, err := addDBfileToDB("http://example.com/valid//url/test.db", "foo"); err != nil {
		t.Errorf("Should have raised no error but got error %v", err)
	}
	dropUnusedDBFiles(oneMonthAgo)
	dbs := getAllMirrorsDB()
	if len(dbs) != 1 {
		t.Errorf("The db should contain %d entries, but it contains %d", 1, len(dbs))
	}
	var mirr MirrorDB
	prefetchDB.Model(&MirrorDB{}).Where("mirror_dbs.url = ? and mirror_dbs.repo_name = ?", "http://example.com/valid//url/test.db", "foo").First(&mirr)
	twoMonthsAgo := time.Now().AddDate(0, -2, 0)
	mirr.LastTimeDownloaded = &twoMonthsAgo
	prefetchDB.Save(&mirr)
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
	updateDBDownloadedFile("foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	updateDBDownloadedFile("foo", "webkit2-2.3.1-1-x86_64.pkg.tar.zst")
	updateDBDownloadedFile("foo", "webkit3-2.4.1-1-x86_64.pkg.tar.zst")
	repoPkg, err := buildRepoPkg("webkit-2.4.1-1-x86_64.pkg.tar.zst", "foo")
	if err != nil {
		t.Fatal(err)
	}
	prefetchDB.Save(&repoPkg)
	// same version, shouldn't be included
	repoPkg, err = buildRepoPkg("webkit3-2.4.1-1-x86_64.pkg.tar.zst", "foo")
	if err != nil {
		t.Fatal(err)
	}
	prefetchDB.Save(&repoPkg)
	got := getPkgsToUpdate()
	want := []PkgToUpdate{PkgToUpdate{PackageName: "webkit", RepoName: "foo", Arch: "x86_64", DownloadURL: "/repo/foo/webkit-2.4.1-1-x86_64"}}
	if !cmp.Equal(got, want) {
		t.Errorf("\ngot  %v\nwant %v", got, want)
	}
}
