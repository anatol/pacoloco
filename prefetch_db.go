package main

import (
	"fmt"
	"log"
	"os"
	"path"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Package is a struct which describes a package that had been downloaded and should be in cache.
// To avoid having to deal with integrity constraints, I have set a composite primary key.
// If performance issues rise, set an ID and add constraints to have packagename+arch unique
type Package struct {
	PackageName         string     `gorm:"primaryKey;not null"`
	Version             string     `gorm:"not null"`
	Arch                string     `gorm:"primaryKey;not null"`
	RepoName            string     `gorm:"primaryKey;not null"`
	LastTimeDownloaded  *time.Time `gorm:"not null"`
	LastTimeRepoUpdated *time.Time `gorm:"not null"`
}

// there are many possible paths for a package, this returns all of the possible ones
func getPackagePaths(pkg Package) []string {
	baseString := path.Join("pkgs", pkg.RepoName, pkg.PackageName+"-"+pkg.Version+"-"+pkg.Arch)
	var pkgPaths []string
	for _, ext := range allowedPackagesExtensions {
		pkgPaths = append(pkgPaths, baseString+ext)
		pkgPaths = append(pkgPaths, baseString+ext+".sig")
	}
	return pkgPaths
}

// MirrorDB is a struct which describes a ".db" link from a mirror.
// It is quite hard to know where db files are, so i'll store them when they are requested
// I assume the other files to download are on the same path of the DB
type MirrorDB struct {
	URL                string     `gorm:"primaryKey;not null"`
	RepoName           string     `gorm:"primaryKey;not null"`
	LastTimeDownloaded *time.Time `gorm:"not null"`
}
type RepoPackage struct {
	PackageName string `gorm:"primaryKey;not null"`
	Version     string `gorm:"not null"`
	Arch        string `gorm:"primaryKey;not null"`
	RepoName    string `gorm:"primaryKey;not null"`
	DownloadURL string `gorm:"not null"`
}

func createRepoTable() error {
	_ = prefetchDB.Migrator().DropTable(&RepoPackage{})
	return prefetchDB.Migrator().CreateTable(&RepoPackage{})
}

func deleteRepoTable() error {
	return prefetchDB.Migrator().DropTable(&RepoPackage{})
}

// Creates the db if it doesn't exist
func createPrefetchDB() {
	if config == nil {
		log.Fatalf("Config have not been parsed yet")
	}
	dbPath := path.Join(config.CacheDir, DefaultDBName)
	exists, err := fileExists(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	if !exists {
		log.Printf("Creating %v", dbPath)
		file, err := os.Create(dbPath) // Create SQLite file
		if err != nil {
			log.Fatal(err)
		}
		file.Close()
		db, err := getDBConnection()
		if err != nil {
			log.Fatal(err)
		}
		db.Migrator().CreateTable(&Package{})
		db.Migrator().CreateTable(&MirrorDB{})
		db.Migrator().CreateTable(&RepoPackage{})
	}
}

func getDBConnection() (*gorm.DB, error) {
	dbPath := path.Join(config.CacheDir, DefaultDBName)
	newLogger := logger.New(
		log.New(os.Stdout, "\r\n", log.LstdFlags), // io writer
		logger.Config{
			SlowThreshold:             time.Second, // Slow SQL threshold
			IgnoreRecordNotFoundError: true,        // Ignore ErrRecordNotFound error for logger
		},
	)

	return gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: newLogger})
}

func getPackage(pkgName string, arch string, reponame string) Package {
	var pkg Package
	if prefetchDB == nil {
		log.Fatalf("Called getPackage with no initialized db!")
	}
	prefetchDB.Model(&Package{}).Where("packages.package_name=? AND packages.arch=? AND packages.repo_name = ?", pkgName, arch, reponame).First(&pkg)
	return pkg
}

// Returns unused packages and removes them from the db
func getAndDropUnusedPackages(period time.Duration) []Package {
	var possiblyUnusedPkgs []Package
	prefetchDB.Model(&Package{}).Where("packages.last_time_repo_updated > packages.last_time_downloaded").Find(&possiblyUnusedPkgs)
	var unusedPkgs []Package
	for _, pkg := range possiblyUnusedPkgs {
		if pkg.LastTimeRepoUpdated.Sub(*pkg.LastTimeDownloaded) > period {
			unusedPkgs = append(unusedPkgs, pkg)
			// GORM issue here with composite keys, only with sqlite3 (yay) https://github.com/go-gorm/gorm/issues/3585
			var tmp []Package
			prefetchDB.Model(&Package{}).Unscoped().Where("packages.package_name = ? and packages.arch = ? and packages.repo_name = ?", pkg.PackageName, pkg.Arch, pkg.RepoName).Delete(&tmp)
		}
	}
	return unusedPkgs
}

// Returns unused db files and removes them from the db
func dropUnusedDBFiles(olderThan time.Time) {
	prefetchDB.Model(&MirrorDB{}).Unscoped().Where("mirror_dbs.last_time_downloaded < ?", olderThan).Delete(&MirrorDB{})
}

// Returns dead packages and removes them from the db
func getAndDropDeadPackages(olderThan time.Time) []Package {
	var deadPkgs []Package
	prefetchDB.Model(&Package{}).Where("packages.last_time_downloaded < ? AND packages.last_time_repo_updated < ?", olderThan, olderThan).Find(&deadPkgs)
	prefetchDB.Model(&Package{}).Unscoped().Where("packages.last_time_downloaded < ? AND packages.last_time_repo_updated < ?", olderThan, olderThan).Delete(&[]Package{})
	return deadPkgs
}

// creates a package from an url
func getPackageFromFilenameAndRepo(repoName string, fileName string) (Package, error) {
	matches := filenameRegex.FindStringSubmatch(fileName)
	if len(matches) >= 7 {
		PackageName := matches[1]
		version := matches[2]
		arch := matches[3]
		now := time.Now()
		return Package{PackageName,
			version,
			arch,
			repoName,
			&now,
			&now,
		}, nil
	}
	return Package{}, fmt.Errorf("package with name '%v' cannot be prefetched cause it doesn't follow the package name formatting regex", fileName)
}

type PkgToUpdate struct {
	PackageName string
	Arch        string
	RepoName    string
	DownloadURL string
}

func getPkgToUpdateDownloadURLs(p PkgToUpdate) []string {
	baseString := p.DownloadURL
	var urls []string
	for _, ext := range allowedPackagesExtensions {
		urls = append(urls, baseString+ext)
		urls = append(urls, baseString+ext+".sig")
	}
	return urls
}

// returns a list of packages which should be prefetched
func getPkgsToUpdate() []PkgToUpdate {
	rows, err := prefetchDB.Model(&Package{}).Joins("inner join repo_packages on repo_packages.package_name = packages.package_name AND repo_packages.arch = packages.arch AND repo_packages.repo_name = packages.repo_name AND repo_packages.version <> packages.version").Select("packages.package_name,packages.arch,packages.repo_name,repo_packages.download_url").Rows()
	if err != nil {
		log.Fatal(err)
	}
	var pkgs []PkgToUpdate
	for rows.Next() {
		var pkg PkgToUpdate
		rows.Scan(&pkg.PackageName, &pkg.Arch, &pkg.RepoName, &pkg.DownloadURL)
		pkgs = append(pkgs, pkg)
	}
	return pkgs
}

// add a complete url of a DB in a db. This urls are used to download afterwards the db to know which packages should be prefetched.
func addDBfileToDB(urlDB string, repoName string) (MirrorDB, error) {
	now := time.Now()
	if prefetchDB == nil {
		log.Fatalf("prefetchDB is uninitialized")
	}
	matches := urlRegex.FindStringSubmatch(urlDB)
	if len(matches) == 0 {
		return MirrorDB{}, fmt.Errorf("url '%v' is invalid, cannot save it for prefetching", urlDB)
	}
	mirror := MirrorDB{urlDB, repoName, &now}
	prefetchDB.Save(&mirror)
	return mirror, nil
}
func getAllMirrorsDB() []MirrorDB {
	var mirrorDBs []MirrorDB
	prefetchDB.Find(&mirrorDBs)
	return mirrorDBs
}

func deleteMirrorDBFromDB(m MirrorDB) {
	prefetchDB.Model(&MirrorDB{}).Unscoped().Where("mirror_dbs.url = ? and mirror_dbs.repo_name = ?", m.URL, m.RepoName)
}
