package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorhill/cronexpr"
)

// Gets a duration from a cron string
func getCronDuration(cronStr string, from time.Time) (time.Duration, error) {
	cron, err := cronexpr.Parse(cronStr)
	if err != nil {
		return 0, err // shouldn't happen cause it is being checked on creation
	}
	nextTick := cron.Next(from)
	if nextTick.IsZero() {
		return 0, fmt.Errorf("there is no next tick")
	}
	return nextTick.Sub(from), nil
}

// Setups the prefetching ticker
func setupPrefetchTicker() *time.Ticker {
	if config.Prefetch == nil {
		log.Fatalf("Called setupPrefetchTicker with config.Prefetch uninitialized")
	}

	duration, err := getCronDuration(config.Prefetch.Cron, time.Now())
	if err != nil {
		log.Print(err)
		return nil
	}
	if duration <= 0 {
		log.Printf("Prefetching is disabled")
		return nil
	}

	ticker := time.NewTicker(duration) // set prefetch as specified in config file
	log.Printf("The prefetching routine will be run on %v", time.Now().Add(duration))
	go func() {
		lastTimeInvoked := time.Time{}
		for range ticker.C {
			if time.Since(lastTimeInvoked) > time.Second {
				prefetchPackages()
				lastTimeInvoked = time.Now()
				now := time.Now()
				duration, err := getCronDuration(config.Prefetch.Cron, time.Now())
				if err == nil && duration > 0 {
					ticker.Reset(duration) // update to the new timing
					log.Printf("On %v the prefetching routine will be run again", now.Add(duration))
				} else {
					ticker.Stop()
					log.Printf("Prefetching disabled")
				}
			} // otherwise ignore it. It happened more than once that this function gets invoked twice for no reason
		}
	}()
	return ticker
}

// initializes the prefetchDB variable, by creating the db if it doesn't exist
func setupPrefetch() {
	createPrefetchDB()
	db, err := getDBConnection()
	if err != nil {
		log.Fatal(err)
	}
	prefetchDB = db
}

// function to update the db when a package is being actively requested
func updateDBRequestedFile(repoName string, fileName string) {
	// don't register when signature gets downloaded, to reduce db accesses
	if strings.HasSuffix(fileName, ".sig") || strings.HasSuffix(fileName, ".db") {
		return
	}

	pkg, err := getPackageFromFilenameAndRepo(repoName, fileName)
	if err != nil {
		log.Printf("error: %v", err)
		// Don't register them if they have a wrong format.
		// The accepted format for a package name is name-version-subversion-arch.pkg.tar.zst
		// otherwise I cannot know if a package has been updated
		return
	}
	if prefetchDB == nil {
		log.Fatal("Trying to insert data into a non-existent db")
	}
	var existentPkg Package
	prefetchDB.First(&existentPkg, "packages.package_name = ? and packages.arch = ? AND packages.repo_name = ?", pkg.PackageName, pkg.Arch, pkg.RepoName)
	if existentPkg.PackageName == "" {
		if db := prefetchDB.Save(&pkg); db.Error != nil {
			log.Printf("db error: %v", db.Error)
		}
	} else {
		if existentPkg.Version == pkg.Version {
			now := time.Now()
			existentPkg.LastTimeDownloaded = &now
			if db := prefetchDB.Save(existentPkg); db.Error != nil {
				log.Printf("db error: %v", db.Error)
			}
		} else {
			// if on a repo there is a different version, we assume it is the most recent one.
			// The one with the bigger version number may be wrong, assuming a corner case in which a downgrade have been done in the upstream mirror.
			// This is not a vulnerability, as the client specifies the version it wants

			// if two mirrors serve 2 different versions of the same package, this could be a issue (it won't cache the result).
			// I hope not, cause it would be nonsensical. If it has some sense, mirror name should be added as a primary key too
			purgePkgIfExists(&existentPkg)
			if db := prefetchDB.Save(pkg); db.Error != nil {
				log.Printf("db error: %v", db.Error)
			}
		}
	}
}

// function to update the db when a package gets prefetched
func updateDBPrefetchedFile(repoName string, fileName string) {
	// don't register when signature gets downloaded, to reduce db accesses
	if strings.HasSuffix(fileName, ".pkg.tar.zst") {
		pkg, err := getPackageFromFilenameAndRepo(repoName, fileName)
		if err != nil {
			log.Printf("error: %v", err)
			return
		}
		if prefetchDB == nil {
			log.Fatal("Trying to insert data into a non-existent db")
		}
		var existentPkg Package
		prefetchDB.First(&existentPkg, "packages.package_name = ? and packages.arch = ? AND packages.repo_name = ?", pkg.PackageName, pkg.Arch, pkg.RepoName)
		if existentPkg.PackageName == "" {
			if db := prefetchDB.Save(&pkg); db.Error != nil {
				log.Printf("db error: %v", db.Error)
			}
			log.Printf("warning: prefetched package wasn't on the db")
			return
		} else {
			if existentPkg.Version == pkg.Version {
				now := time.Now()
				existentPkg.LastTimeRepoUpdated = &now
				if db := prefetchDB.Save(existentPkg); db.Error != nil {
					log.Printf("db error: %v", db.Error)
				}
			} else {
				// if on a repo there is a different version, we assume it is the most recent one.
				// The one with the bigger version number may be wrong, assuming a corner case in which a downgrade have been done in the upstream mirror.
				// This is not a vulnerability, as the client specifies the version it wants

				// if two mirrors serve 2 different versions of the same package, this could be (a bit of an) issue cause
				// pacoloco won't cache the result.
				// I hope not, because it would be nonsensical. If it has some sense, mirror name should be added as a primary key too
				purgePkgIfExists(&existentPkg)
				if db := prefetchDB.Save(pkg); db.Error != nil {
					log.Printf("db error: %v", db.Error)
				}
			}
		}
	}
}

// purges all possible package files
func purgePkgIfExists(pkgToDel *Package) {
	if pkgToDel == nil {
		return
	}
	for _, p := range pkgToDel.getAllPaths() {
		pathToDelete := filepath.Join(config.CacheDir, p)
		if err := os.Remove(pathToDelete); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("Error while trying to remove unused package %v : %v", pathToDelete, err)
		}
	}
}

// purges unused and dead packages both from db and their files and removes unused db links from the db
func cleanPrefetchDB() {
	log.Printf("Cleaning the db...")
	if config.Prefetch == nil {
		log.Fatalf("Shouldn't call a prefetch purge when prefetch is not set in the yaml. This is most likely a bug.")
	}
	period := 24 * time.Hour * time.Duration(config.Prefetch.TTLUnaccessed)
	olderThan := time.Now().Add(-period)
	deadPkgs := getAndDropUnusedPackages(period)
	dropUnusedDBFiles(olderThan) // drop too old db links
	// deletes unused pkgs
	for _, pkgToDel := range deadPkgs {
		purgePkgIfExists(&pkgToDel)
	}
	period = 24 * time.Hour * time.Duration(config.Prefetch.TTLUnupdated)
	olderThan = time.Now().Add(-period)
	deadPkgs = getAndDropDeadPackages(olderThan)
	// deletes dead packages
	for _, pkgToDel := range deadPkgs {
		purgePkgIfExists(&pkgToDel)
	}
	// delete mirror links which does not exist on the config file or are invalid
	mirrors := getAllMirrorsDB()
	for _, mirror := range mirrors {
		if _, exists := config.Repos[mirror.RepoName]; exists {
			if !strings.HasPrefix(mirror.URL, "/repo/") {
				log.Printf("warning: deleting %v link due to migrating to a newer version of pacoloco. Simply do 'pacman -Sy' on repo %v to fix the prefetching.", mirror.URL, mirror.RepoName)
				deleteMirrorDBFromDB(mirror)
			}
		} else {
			// there is no repo with that name, I delete the mirrorDB entry
			log.Printf("Deleting %v, repo %v does not exist", mirror.URL, mirror.RepoName)
			deleteMirrorDBFromDB(mirror)
		}
	}

	// should be useless but this guarantees that everything got cleaned properly
	_ = deleteMirrorPkgsTable()
	log.Printf("Db cleaned.")
}

// This calls the actual prefetching process, should be called once the db had been cleaned
func prefetchAllPkgs() {
	updateMirrorsDbs()
	defer deleteMirrorPkgsTable()
	pkgs, err := getPkgsToUpdate()
	if err != nil {
		log.Printf("Prefetching failed: %v. Are you sure you had something to prefetch?", err)
		return
	}
	for _, p := range pkgs {
		pkg := getPackage(p.PackageName, p.Arch, p.RepoName)
		urls := p.getDownloadURLs()
		var failed []string
		for _, url := range urls {
			if err := prefetchRequest(url, ""); err != nil {
				failed = append(failed, fmt.Sprintf("Failed to prefetch package at %v because %v", url, err))
				continue
			}
			purgePkgIfExists(&pkg) // delete the old package
			if strings.HasSuffix(url, ".sig") {
				log.Printf("Successfully prefetched %v-%v signature", p.PackageName, p.Arch)
			} else {
				log.Printf("Successfully prefetched %v-%v package", p.PackageName, p.Arch)
			}
		}
		if len(urls)-len(failed) < 2 { // If less than 2 packages succeeded in being downloaded, show error messages
			for _, msg := range failed {
				log.Println(msg)
			}
		}
	}
}

// the prefetching routine
func prefetchPackages() {
	if prefetchDB == nil {
		return
	}
	log.Printf("Starting prefetching routine...")
	// update mirrorlists from file if they exist
	// purge all useless files
	cleanPrefetchDB()
	// prefetch all Packages
	log.Printf("Starting prefetching packages...")
	prefetchAllPkgs()
	log.Printf("Finished prefetching packages!")
	log.Printf("Finished prefetching routine!")
}
