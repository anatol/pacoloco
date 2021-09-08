package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// helper to setup the db
func testSetupHelper(t *testing.T) string {
	notInvokingPrefetchTime := time.Now().Add(-(time.Duration(time.Hour))) // an hour ago
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

func testDbConnectionHelper(filepath string) *sql.DB {
	db, err := sql.Open("sqlite3", filepath)
	if err != nil {
		panic(err)
	}
	if db == nil {
		panic("db nil")
	}
	return db
}
func TestSetupPrefetch(t *testing.T) {
	tmpDir := testSetupHelper(t)
	setupPrefetch()
	exists, err := fileExists(path.Join(tmpDir, DefaultDBName))
	if err != nil {
		log.Fatal(err)
	}
	if !exists {
		t.Fatal("setupPrefetch didn't create the db file")
	}
	if prefetchDB == nil {
		t.Errorf("Prefetch DB is uninitilized")
	}
	conn := testDbConnectionHelper(path.Join(tmpDir, DefaultDBName))
	for _, table := range []string{"mirror_dbs", "packages", "repo_packages"} {
		res, err := conn.Query("select * from " + table)
		if err != nil {
			log.Fatal(err)
		}
		for res.Next() {
			var pkg Package
			err = res.Scan(&pkg.PackageName, &pkg.Version, &pkg.Arch, &pkg.RepoName, &pkg.LastTimeDownloaded, &pkg.LastTimeRepoUpdated)
			fmt.Print(pkg)
			if err != nil {
				log.Fatal(err)
			}
			t.Fatalf("setupPrefetch shouldn't create entries in %v\n", table)
		}
	}
}
func TestSetupPrefetchTicker(t *testing.T) {
	testSetupHelper(t)
	setupPrefetch()
	ticker := setupPrefetchTicker()
	if ticker == nil {
		t.Errorf("returned ticker shouldn't be nil.")
	}
	ticker.Stop()
}

func TestUpdateDBDownloadedFile(t *testing.T) {
	tmpDir := testSetupHelper(t)
	setupPrefetch()
	conn := testDbConnectionHelper(path.Join(tmpDir, DefaultDBName))
	updateDBDownloadedFile("nope", "Wrongfile.db")
	updateDBDownloadedFile("nope", "Wrongfile.zst")
	updateDBDownloadedFile("nope", "fakeacceptablefile.pkg.tar.zst")     // doesn't have the correct format
	updateDBDownloadedFile("nope", "acl-2.3.1-1-x86_64.pkg.tar.zst.sig") // do not save signatures too in the db
	// none of those should be in the db, now i'll check
	for _, table := range []string{"mirror_dbs", "packages", "repo_packages"} {
		res, err := conn.Query("select * from " + table)
		if err != nil {
			log.Fatal(err)
		}
		for res.Next() {
			var pkg Package
			err = res.Scan(&pkg.PackageName, &pkg.Version, &pkg.Arch, &pkg.RepoName, &pkg.LastTimeDownloaded, &pkg.LastTimeRepoUpdated)
			fmt.Print(pkg)
			if err != nil {
				log.Fatal(err)
			}
			t.Fatalf("updateDBDownloadedFile shouldn't create entries in %v with bad values\n", table)
		}
	}
	// this one should be added
	updateDBDownloadedFile("foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	res, err := conn.Query("select * from packages")
	if err != nil {
		log.Fatal(err)
	}
	if res.Next() {
		var got Package
		now := time.Now()
		err = res.Scan(&got.PackageName, &got.Version, &got.Arch, &got.RepoName, &got.LastTimeDownloaded, &got.LastTimeRepoUpdated)

		want := Package{PackageName: "webkit", Version: "2.3.1-1", Arch: "x86_64", RepoName: "foo", LastTimeDownloaded: &now, LastTimeRepoUpdated: &now}
		if !cmp.Equal(got, want, cmpopts.IgnoreFields(Package{}, "LastTimeDownloaded", "LastTimeRepoUpdated")) {
			t.Errorf("\ngot  %v,\nwant %v", got, want)
		}
		dist := want.LastTimeDownloaded.Sub(*got.LastTimeDownloaded)
		if dist < -5*time.Second {
			t.Errorf("Unexpected result, got.LastTimeDownloaded is wrong")
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
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	t.Fatalf("updateDBDownloadedFile should create entries in packages\n")
	// this one should replace the previous one
	updateDBDownloadedFile("foo", "webkit-2.5.1-1-x86_64.pkg.tar.zst")
	// supposing two people downloaded it at the same time
	updateDBDownloadedFile("foo", "webkit-2.5.1-1-x86_64.pkg.tar.zst")
	res, err = conn.Query("select * from packages")
	if err != nil {
		log.Fatal(err)
	}
	counter := 0
	for res.Next() {
		var got Package
		now := time.Now()
		err = res.Scan(&got.PackageName, &got.Version, &got.Arch, &got.RepoName, &got.LastTimeDownloaded, &got.LastTimeRepoUpdated)

		want := Package{PackageName: "webkit", Version: "2.5.1-1", Arch: "x86_64", RepoName: "foo", LastTimeDownloaded: &now, LastTimeRepoUpdated: &now}
		if !cmp.Equal(got, want, cmpopts.IgnoreFields(Package{}, "LastTimeDownloaded", "LastTimeRepoUpdated")) {
			t.Errorf("\ngot  %v,\nwant %v", got, want)
		}
		dist := want.LastTimeDownloaded.Sub(*got.LastTimeDownloaded)
		if dist < -5*time.Second {
			t.Errorf("Unexpected result, got.LastTimeDownloaded is wrong")
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
		if err != nil {
			log.Fatal(err)
		}
		counter++
		if counter > 1 {
			t.Fatalf("updateDBDownloadedFile shouldn't have created multiple entries\n")
		}
	}

}

func TestUpdateDBPrefetchedFile(t *testing.T) {
	tmpDir := testSetupHelper(t)
	setupPrefetch()
	conn := testDbConnectionHelper(path.Join(tmpDir, DefaultDBName))
	// add a fake download entry
	updateDBDownloadedFile("foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	oldPkgPath := path.Join(tmpDir, "pkgs", "foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	if err := os.MkdirAll(path.Join(tmpDir, "pkgs"), 0755); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(path.Join(tmpDir, "pkgs", "foo"), 0755); err != nil {
		log.Fatal(err)
	}
	if _, err := os.Create(oldPkgPath); err != nil {
		log.Fatal(err)
	}
	if _, err := os.Create(oldPkgPath + ".sig"); err != nil {
		log.Fatal(err)
	}

	// simulate a new prefetched file
	newPkgPath := path.Join(tmpDir, "pkgs", "foo", "webkit-2.5.10-4-x86_64.pkg.tar.zst")
	if _, err := os.Create(newPkgPath); err != nil {
		log.Fatal(err)
	}
	if _, err := os.Create(newPkgPath + ".sig"); err != nil {
		log.Fatal(err)
	}
	updateDBPrefetchedFile("foo", "webkit-2.5.10-4-x86_64.pkg.tar.zst")
	// check if it properly exists
	res, err := conn.Query("select * from packages WHERE packages.package_name='webkit' AND packages.arch='x86_64' AND packages.repo_name='foo'")
	if err != nil {
		log.Fatal(err)
	}
	counter := 0
	for res.Next() {
		var got Package
		now := time.Now()
		err = res.Scan(&got.PackageName, &got.Version, &got.Arch, &got.RepoName, &got.LastTimeDownloaded, &got.LastTimeRepoUpdated)

		want := Package{PackageName: "webkit", Version: "2.5.10-4", Arch: "x86_64", RepoName: "foo", LastTimeDownloaded: &now, LastTimeRepoUpdated: &now}
		if !cmp.Equal(got, want, cmpopts.IgnoreFields(Package{}, "LastTimeDownloaded", "LastTimeRepoUpdated")) {
			t.Errorf("\ngot  %v,\nwant %v", got, want)
		}
		dist := want.LastTimeDownloaded.Sub(*got.LastTimeDownloaded)
		if dist < -5*time.Second {
			t.Errorf("Unexpected result, got.LastTimeDownloaded is wrong")
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
		if err != nil {
			log.Fatal(err)
		}
		if counter >= 1 {
			t.Errorf("Too many entries, expected %d, found %d", 1, counter+1)
		}
		counter++
	}
	if counter == 0 {
		t.Errorf("Too few entries, expected %d, found %d", 1, counter+1)
	}
	// now, check if files have been properly handled
	exists, err := fileExists(oldPkgPath)
	if err != nil {
		log.Fatal(err)
	}
	if exists {
		t.Errorf("File %v should have been deleted", oldPkgPath)
	}
	exists, err = fileExists(oldPkgPath + ".sig")
	if err != nil {
		log.Fatal(err)
	}
	if exists {
		t.Errorf("File %v should have been deleted", oldPkgPath+".sig")
	}
	exists, err = fileExists(newPkgPath + ".sig")
	if err != nil {
		log.Fatal(err)
	}
	if !exists {
		t.Errorf("File %v should exist", newPkgPath+".sig")
	}
	exists, err = fileExists(newPkgPath)
	if err != nil {
		log.Fatal(err)
	}
	if !exists {
		t.Errorf("File %v should exist", newPkgPath)
	}
}

func TestPurgePkgIfExists(t *testing.T) {
	tmpDir := testSetupHelper(t)
	setupPrefetch()
	updateDBDownloadedFile("foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	oldPkgPath := path.Join(tmpDir, "pkgs", "foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	if err := os.MkdirAll(path.Join(tmpDir, "pkgs"), 0755); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(path.Join(tmpDir, "pkgs", "foo"), 0755); err != nil {
		log.Fatal(err)
	}
	if _, err := os.Create(oldPkgPath); err != nil {
		log.Fatal(err)
	}
	if _, err := os.Create(oldPkgPath + ".sig"); err != nil {
		log.Fatal(err)
	}
	if _, err := os.Create(oldPkgPath + ".ssig"); err != nil {
		log.Fatal(err)
	}
	pkgToPurge := getPackage("webkit", "x86_64", "foo")
	purgePkgIfExists(&pkgToPurge)
	// now, check if files have been properly handled
	exists, err := fileExists(oldPkgPath)
	if err != nil {
		log.Fatal(err)
	}
	if exists {
		t.Errorf("File %v should have been deleted", oldPkgPath)
	}
	exists, err = fileExists(oldPkgPath + ".sig")
	if err != nil {
		log.Fatal(err)
	}
	if exists {
		t.Errorf("File %v should have been deleted", oldPkgPath+".sig")
	}
	exists, err = fileExists(oldPkgPath + ".ssig")
	if err != nil {
		log.Fatal(err)
	}
	if !exists {
		t.Errorf("File %v should exist", oldPkgPath+".ssig")
	}
}

func TestCleanPrefetchDB(t *testing.T) {
	tmpDir := testSetupHelper(t)
	setupPrefetch()
	updateDBDownloadedFile("foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	oldPkgPath := path.Join(tmpDir, "pkgs", "foo", "webkit-2.3.1-1-x86_64.pkg.tar.zst")
	if err := os.MkdirAll(path.Join(tmpDir, "pkgs"), 0755); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(path.Join(tmpDir, "pkgs", "foo"), 0755); err != nil {
		log.Fatal(err)
	}
	if _, err := os.Create(oldPkgPath); err != nil {
		log.Fatal(err)
	}
	if _, err := os.Create(oldPkgPath + ".sig"); err != nil {
		log.Fatal(err)
	}
	// created some files
	cleanPrefetchDB()
	// should delete nothing
	exists, err := fileExists(oldPkgPath + ".sig")
	if err != nil {
		log.Fatal(err)
	}
	if !exists {
		t.Errorf("File %v should exist", oldPkgPath+".sig")
	}
	exists, err = fileExists(oldPkgPath)
	if err != nil {
		log.Fatal(err)
	}
	if !exists {
		t.Errorf("File %v should exist", oldPkgPath)
	}
	// now i update some of its data
	pkg := getPackage("webkit", "x86_64", "foo")
	oneMonthAgo := time.Now().AddDate(0, -1, 0) // more or less
	// updated one month ago but downloaded now, should not be deleted
	pkg.LastTimeRepoUpdated = &oneMonthAgo
	prefetchDB.Save(pkg)
	cleanPrefetchDB()
	// should delete nothing
	latestPkgInDB := getPackage("webkit", "x86_64", "foo")
	if pkg.PackageName != latestPkgInDB.PackageName || pkg.Arch != latestPkgInDB.Arch || pkg.RepoName != latestPkgInDB.RepoName {
		t.Errorf("Package shouldn't be altered, was \n%v, now it is \n%v", pkg, latestPkgInDB)
	}
	exists, err = fileExists(oldPkgPath + ".sig")
	if err != nil {
		log.Fatal(err)
	}
	if !exists {
		t.Errorf("File %v should exist", oldPkgPath+".sig")
	}
	exists, err = fileExists(oldPkgPath)
	if err != nil {
		log.Fatal(err)
	}
	if !exists {
		t.Errorf("File %v should exist", oldPkgPath)
	}
	// now it should be deleted, knowing that a package is dead (in this configuration) if older than 20 days

	pkg.LastTimeDownloaded = &oneMonthAgo
	prefetchDB.Save(pkg)
	cleanPrefetchDB()
	latestPkgInDB = getPackage("webkit", "x86_64", "foo")
	if latestPkgInDB.PackageName != "" && pkg.Arch != "" {
		t.Errorf("Package should have been deleted")
	}
	exists, err = fileExists(oldPkgPath + ".sig")
	if err != nil {
		log.Fatal(err)
	}
	if exists {
		t.Errorf("File %v should not exist", oldPkgPath+".sig")
	}
	exists, err = fileExists(oldPkgPath)
	if err != nil {
		log.Fatal(err)
	}
	if exists {
		t.Errorf("File %v not should exist", oldPkgPath)
	}

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

func TestGetPrefetchDuration(t *testing.T) {
	now := time.Now()
	var expectedTime time.Time
	if now.Hour() < 3 {
		expectedTime = now
	} else {
		expectedTime = now.AddDate(0, 0, 1)
	}
	expectedTime = time.Date(expectedTime.Year(), expectedTime.Month(), expectedTime.Day(), 3, 0, 0, 0, expectedTime.Location())
	expectedDuration := expectedTime.Sub(now)
	got, err := getPrefetchDuration("0 0 3 * * * *", now)
	if err != nil {
		t.Errorf("Unexpected error %v", err)
	}
	if got != expectedDuration {
		t.Errorf("getPrefetchDuration() = %v, want %v", got, expectedDuration)
	}

}
