package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

func updateMirrorlists() {
	for name, repo := range config.Repos {
		err := checkAndUpdateMirrorlistRepo(name, repo)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func checkAndUpdateMirrorlistRepo(repoName string, repo *Repo) error {
	if repo.Mirrorlist != "" {
		repo.Mutex.Lock()
		defer repo.Mutex.Unlock()

		if time.Since(repo.LastMirrorlistCheck) < 5*time.Second {
			// if there is an entry in the lastMirrorlistCheck and that entry has a distance lower than 5 seconds from now, don't update its mirrorlist
			return nil
		}

		repo.LastMirrorlistCheck = time.Now()
		err := updateRepoMirrorlist(repoName, repo)
		if err != nil {
			return fmt.Errorf("error while updating %v repo mirrorlist: %v", repoName, err)
		}
	}
	return nil
}

func updateRepoMirrorlist(repoName string, repo *Repo) error {
	fileInfo, err := os.Stat(repo.Mirrorlist)
	if err != nil {
		return err
	}

	fileModTime := fileInfo.ModTime()
	if fileModTime == repo.LastModificationTime {
		return nil
	}

	repo.LastModificationTime = fileModTime

	// open readonly, it won't change modification time
	file, err := os.Open(repo.Mirrorlist)
	if err != nil {
		return err
	}
	defer file.Close()
	// initialize the urls collection
	repo.URLs = make([]string, 0)
	scanner := bufio.NewScanner(file)
	// resize scanner's capacity if lines are longer than 64K.
	for scanner.Scan() {
		matches := mirrorlistRegex.FindStringSubmatch(scanner.Text())
		if len(matches) > 0 {
			url := matches[1]
			if !strings.Contains(url, "$") {
				repo.URLs = append(repo.URLs, url)
			} else {
				// this can be a regex error or otherwise a very peculiar url
				log.Printf("warning: %v url in repo %v contains suspicious characters, skipping it", url, repoName)
			}

		}
		// skip invalid lines
	}
	if len(repo.URLs) == 0 {
		return fmt.Errorf("mirrorlist for repo %v is either empty or isn't a mirrorlist file", repoName)
	}

	return scanner.Err()
}
