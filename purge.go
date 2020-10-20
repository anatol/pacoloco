package main

import (
	"log"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func setupPurgeStaleFilesRoutine() *time.Ticker {
	ticker := time.NewTicker(time.Duration(24) * time.Hour) // purge files once a day
	go func() {
		purgeStaleFiles(config.CacheDir, config.PurgeFilesAfter)
		for {
			select {
			case <-ticker.C:
				purgeStaleFiles(config.CacheDir, config.PurgeFilesAfter)
			}
		}
	}()

	return ticker
}

// purgeStaleFiles purges files in the pacoloco cache
// it recursively scans `cacheDir`/pkgs and if the file access time is older than
// `now` - purgeFilesAfter(seconds) then the file gets removed
func purgeStaleFiles(cacheDir string, purgeFilesAfter int) {
	// safety check, so we don't unintentionally wipe the whole cache
	if purgeFilesAfter == 0 {
		log.Fatalf("Stopping because purgeFilesAfter=%v and that would purge the whole cache", purgeFilesAfter)
	}

	removeIfOlder := time.Now().Add(time.Duration(-purgeFilesAfter) * time.Second)
	pkgDir := filepath.Join(cacheDir, "pkgs")

	// Go through all files in the repos, and check if access time is older than `removeIfOlder`
	walkfn := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		atimeUnix := info.Sys().(*syscall.Stat_t).Atim
		// Note that int64() is needed here, otherwise compilation fails at 32 bit platforms like armv6h. See issue #18.
		atime := time.Unix(int64(atimeUnix.Sec), int64(atimeUnix.Nsec))
		if atime.Before(removeIfOlder) {
			log.Printf("Remove stale file %v as its access time (%v) is too old", path, atime)
			err := os.Remove(path)
			if err != nil {
				log.Print(err)
			}
		}
		return nil
	}
	if err := filepath.Walk(pkgDir, walkfn); err != nil {
		log.Println(err)
	}
}
