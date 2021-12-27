package main

import (
	"fmt"
	"log"
	"os"
	"path"
	"strings"
	"time"

	"github.com/gorhill/cronexpr"
)

// Gets a duration from a cron string
func getPrefetchDuration(cronStr string, from time.Time) (time.Duration, error) {
	cron, err := cronexpr.Parse(cronStr)
	if err != nil {
		return time.Duration(0), err // shouldn't happen cause it is being checked on creation
	}
	nextTick := cron.Next(from)
	if !nextTick.IsZero() {
		duration := nextTick.Sub(from)
		return duration, err
	}
	return time.Duration(0), fmt.Errorf("there is no next tick")
}

// Setups the prefetching ticker
func setupPrefetchTicker() *time.Ticker {
	if config.Prefetch != nil {
		duration, err := getPrefetchDuration(config.Prefetch.Cron, time.Now())
		if err == nil && duration > 0 {
			ticker := time.NewTicker(duration) // set prefetch as specified in config file
			log.Printf("The prefetching routine will be run on %v", time.Now().Add(duration))
			go func() {
				lastTimeInvoked := time.Time{}
				for range ticker.C {
					if time.Since(lastTimeInvoked) > time.Second {
						prefetchPackages()
						lastTimeInvoked = time.Now()
						now := time.Now()
						duration, err := getPrefetchDuration(config.Prefetch.Cron, time.Now())
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
		log.Printf("Prefetching is disabled")
		return nil
	}
	log.Fatalf("Called setupPrefetchTicker with config.Prefetch uninitialized")
	return nil
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
	if !strings.HasSuffix(fileName, ".sig") && !strings.HasSuffix(fileName, ".db") {
		pkg, err := getPackageFromFilenameAndRepo(repoName, fileName)
		if err != nil {
			log.Printf("error: %v\n", err)
			// Don't register them if they have a wrong format.
			// The accepted format for a package name is name-version-subversion-arch.pkg.tar.zst
			// otherwise I cannot know if a package has been updated
			return
		}
		if prefetchDB != nil {
			var existentPkg Package
			prefetchDB.First(&existentPkg, "packages.package_name = ? and packages.arch = ? AND packages.repo_name = ?", pkg.PackageName, pkg.Arch, pkg.RepoName)
			if existentPkg.PackageName == "" {
				if db := prefetchDB.Save(&pkg); db.Error != nil {
					log.Printf("db error: %v\n", db.Error)
				}
			} else {
				if existentPkg.Version == pkg.Version {
					now := time.Now()
					existentPkg.LastTimeDownloaded = &now
					if db := prefetchDB.Save(existentPkg); db.Error != nil {
						log.Printf("db error: %v\n", db.Error)
					}
				} else {
					// if on a repo there is a different version, we assume it is the most recent one.
					// The one with the bigger version number may be wrong, assuming a corner case in which a downgrade have been done in the upstream mirror.
					// This is not a vulnerability, as the client specifies the version it wants

					// if two mirrors serve 2 different versions of the same package, this could be a issue (it won't cache the result).
					// I hope not, cause it would be nonsensical. If it has some sense, mirror name should be added as a primary key too
					purgePkgIfExists(&existentPkg)
					if db := prefetchDB.Save(pkg); db.Error != nil {
						log.Printf("db error: %v\n", db.Error)
					}
				}
			}
		} else {
			log.Fatal("Trying to insert data into a non-existent db")
		}
	}
}

// function to update the db when a package gets prefetched
func updateDBPrefetchedFile(repoName string, fileName string) {
	// don't register when signature gets downloaded, to reduce db accesses
	if strings.HasSuffix(fileName, ".pkg.tar.zst") {
		pkg, err := getPackageFromFilenameAndRepo(repoName, fileName)
		if err != nil {
			log.Printf("error: %v\n", err)
			return
		}
		if prefetchDB != nil {
			var existentPkg Package
			prefetchDB.First(&existentPkg, "packages.package_name = ? and packages.arch = ? AND packages.repo_name = ?", pkg.PackageName, pkg.Arch, pkg.RepoName)
			if existentPkg.PackageName == "" {
				if db := prefetchDB.Save(&pkg); db.Error != nil {
					log.Printf("db error: %v\n", db.Error)
				} // save it anyway
				if err := fmt.Errorf("warning: prefetched package wasn't on the db"); err != nil {
					log.Printf("error: %v\n", err)
					return
				}
			} else {
				if existentPkg.Version == pkg.Version {
					now := time.Now()
					existentPkg.LastTimeRepoUpdated = &now
					if db := prefetchDB.Save(existentPkg); db.Error != nil {
						log.Printf("db error: %v\n", db.Error)
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
						log.Printf("db error: %v\n", db.Error)
					}
				}
			}
		} else {
			log.Fatal("Trying to insert data into a non-existent db")
		}
	}
}

// purges all possible package files
func purgePkgIfExists(pkgToDel *Package) {
	if pkgToDel != nil {
		basePathsToDelete := getAllPackagePaths(*pkgToDel)
		for _, p := range basePathsToDelete {
			pathToDelete := path.Join(config.CacheDir, p)
			if _, err := os.Stat(pathToDelete); !os.IsNotExist(err) {
				// if it exists, delete it
				if err := os.Remove(pathToDelete); err != nil {
					log.Printf("Error while trying to remove unused package %v : %v", pathToDelete, err)
				}
			}
		}
	}
}

// purges unused and dead packages both from db and their files and removes unused db links from the db
func cleanPrefetchDB() {
	log.Printf("Cleaning the db...\n")
	if config.Prefetch != nil {
		period := time.Duration(24 * int64(time.Hour) * int64(config.Prefetch.TTLUnaccessed))
		olderThan := time.Now().Add(-period)
		deadPkgs := getAndDropUnusedPackages(period)
		dropUnusedDBFiles(olderThan) // drop too old db links
		// deletes unused pkgs
		for _, pkgToDel := range deadPkgs {
			purgePkgIfExists(&pkgToDel)
		}
		period = time.Duration(24 * int64(time.Hour) * int64(config.Prefetch.TTLUnupdated))
		olderThan = time.Now().Add(-period)
		deadPkgs = getAndDropDeadPackages(olderThan)
		// deletes dead packages
		for _, pkgToDel := range deadPkgs {
			purgePkgIfExists(&pkgToDel)
		}
		// delete mirror links which does not exist on the config file
		mirrors := getAllMirrorsDB()
		for _, mirror := range mirrors {
			if repoLinks, exists := config.Repos[mirror.RepoName]; exists {
				var URLs []string
				if repoLinks.URL != "" {
					URLs = append(URLs, repoLinks.URL)
				} else {
					URLs = repoLinks.URLs
				}
				// compare the mirror URL with the URLs in the config file
				found := false
				for _, URL := range URLs {
					if strings.Contains(mirror.URL, URL) {
						found = true
						break
					}
				}
				if !found {
					log.Printf("Deleting %v, mirror not found on config file", mirror.URL)
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
		log.Printf("Db cleaned.\n")
	} else {
		log.Fatalf("Shouldn't call a prefetch purge when prefetch is not set in the yaml. This is most likely a bug.")
	}
}

// This calls the actual prefetching process, should be called once the db had been cleaned
func prefetchAllPkgs() {
	updateMirrorsDbs()
	defer deleteMirrorPkgsTable()
	pkgs := getPkgsToUpdate()
	for _, p := range pkgs {
		pkg := getPackage(p.PackageName, p.Arch, p.RepoName)
		urls := getPkgToUpdateDownloadURLs(p)
		var failed []string
		for _, url := range urls {
			if err := prefetchRequest(url); err == nil {
				purgePkgIfExists(&pkg) // delete the old package
				if strings.HasSuffix(url, ".sig") {
					log.Printf("Successfully prefetched %v-%v signature\n", p.PackageName, p.Arch)
				} else {
					log.Printf("Successfully prefetched %v-%v package\n", p.PackageName, p.Arch)
				}
			} else {
				failed = append(failed, fmt.Sprintf("Failed to prefetch package at %v because %v\n", url, err))
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
	if prefetchDB != nil {
		log.Printf("Starting prefetching routine...\n")
		// purge all useless files
		cleanPrefetchDB()
		// prefetch all Packages
		log.Printf("Starting prefetching packages...\n")
		prefetchAllPkgs()
		log.Printf("Finished prefetching packages!\n")
		log.Printf("Finished prefetching routine!\n")
	}
}
