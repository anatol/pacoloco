package main

import (
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/djherbis/times"
)

func setupPurgeStaleFilesRoutine() *time.Ticker {
	ticker := time.NewTicker(time.Duration(24) * time.Hour) // purge files once a day
	go func() {
		for repoName := range config.Repos {
			purgeStaleFiles(config.CacheDir, config.PurgeFilesAfter, repoName)
		}
		for {
			select {
			case <-ticker.C:
				for repoName := range config.Repos {
					purgeStaleFiles(config.CacheDir, config.PurgeFilesAfter, repoName)
				}
			}
		}
	}()

	return ticker
}

// purgeStaleFiles purges files in the pacoloco cache
// it recursively scans `cacheDir`/pkgs and if the file access time is older than
// `now` - purgeFilesAfter(seconds) then the file gets removed
func purgeStaleFiles(cacheDir string, purgeFilesAfter int, repoName string) {
	// safety check, so we don't unintentionally wipe the whole cache
	if purgeFilesAfter == 0 {
		log.Fatalf("Stopping because purgeFilesAfter=%v and that would purge the whole cache", purgeFilesAfter)
	}

	removeIfOlder := time.Now().Add(time.Duration(-purgeFilesAfter) * time.Second)
	pkgDir := filepath.Join(cacheDir, "pkgs", repoName)
	var packageSize int64
	var packageNum int64
	// Go through all files in the repos, and check if access time is older than `removeIfOlder`
	walkfn := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		t := times.Get(info)
		atime := t.AccessTime()
		if atime.Before(removeIfOlder) {
			log.Printf("Remove stale file %v as its access time (%v) is too old", path, atime)
			if err := os.Remove(path); err != nil {
				log.Print(err)
			}
		} else {
			packageSize += info.Size()
			packageNum++
		}
		return nil
	}
	if err := filepath.Walk(pkgDir, walkfn); err != nil {
		log.Println(err)
	}
	cachePackageGauge.WithLabelValues(repoName).Set(float64(packageNum))
	cacheSizeGauge.WithLabelValues(repoName).Set(float64(packageSize))
}
