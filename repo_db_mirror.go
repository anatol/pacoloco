package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Uncompresses a gzip file
// I don't know how to set some limits to avoid OOM with gzip bombs.
// Shouldn't happen but could happen if a mirror gets compromised
func uncompressGZ(filePath string, targetFile string) error {
	gzipfile, err := os.Open(filePath)
	if err != nil {
		log.Printf("error: %v\n", err)
		return err
	}
	reader, err := gzip.NewReader(gzipfile)
	if err != nil {
		log.Printf("error: %v\n", err)
		return err
	}
	defer reader.Close()
	writer, err := os.Create(targetFile)
	if err != nil {
		log.Printf("error: %v\n", err)
		return err
	}
	defer writer.Close()
	if _, err = io.Copy(writer, reader); err != nil {
		log.Printf("error: %v\n", err)
		return err
	}
	return nil
}

func extractFilenamesFromTar(filePath string) ([]string, error) {
	f, err := os.Open(filePath)
	reader := bufio.NewReader(f)
	if err != nil {
		log.Printf("error: %v\n", err)
		return []string{}, err
	} // die quietly
	var pkgList []string
	tr := tar.NewReader(reader)
	if err != nil {
		log.Printf("error: %v\n", err)
		return []string{}, err
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break // End of archive
		}
		if strings.HasSuffix(hdr.Name, "/desc") {
			buf := new(strings.Builder)
			if _, err = io.Copy(buf, tr); err != nil {
				log.Printf("error: %v\n", err)
				return []string{}, err
			}
			pkgName := buf.String()
			matches := filenameDBRegex.FindStringSubmatch(pkgName) // find %FILENAME% and read the following string
			if len(matches) == 2 {
				pkgName = matches[1]
				pkgList = append(pkgList, pkgName)
			} else {
				log.Printf("Skipping %v cause it doesn't match regex. This is probably a bug.", hdr.Name)
				continue
			}
		}
	}
	return pkgList, nil
}

// This function returns a url which should download the exactly identical pkg when sent to pacoloco except for the file extension
func getPacolocoURL(pkg Package, prefix string) string {
	return strings.ReplaceAll(("/repo/" + pkg.RepoName + "/" + prefix + "/" + pkg.PackageName + "-" + pkg.Version + "-" + pkg.Arch), "//", "/")
}

// Builds a repository package
// It requires the prefix, which is the relative path in which the db is contained
func buildRepoPkg(fileName string, repoName string, prefix string) (RepoPackage, error) {
	matches := filenameRegex.FindStringSubmatch(fileName)
	if len(matches) >= 7 {
		packageName := matches[1]
		version := matches[2]
		arch := matches[3]
		pkg := Package{PackageName: packageName, Version: version, Arch: arch, RepoName: repoName}
		pacolocoURL := getPacolocoURL(pkg, prefix)
		return RepoPackage{PackageName: packageName, Version: version, Arch: arch, DownloadURL: pacolocoURL, RepoName: repoName}, nil
	}
	return RepoPackage{}, fmt.Errorf("filename %v does not match regex, matches length is %d", fileName, len(matches))
}

// Returns the "path" field from a mirror url, e.g. from
// https://mirror.example.com/mirror/packages/archlinux//extra/os/x86_64/extra.db
// it extracts /extra/os/x86_64
func getPrefixFromMirrorDB(mirror MirrorDB) (string, error) {
	if repoLinks, exists := config.Repos[mirror.RepoName]; exists {
		var URLs []string
		if repoLinks.URL != "" {
			URLs = append(URLs, repoLinks.URL)
		} else {
			URLs = repoLinks.URLs
		}
		for _, URL := range URLs {
			splittedURL := strings.Split(mirror.URL, URL)
			if len(splittedURL) <= 1 {
				continue // this is not the proper url
			}
			matches := mirrorDBRegex.FindStringSubmatch(splittedURL[1])
			if len(matches) < 1 {
				// It means that the path is empty, e.g. //extra.db or extra.db
				return "", nil
			}
			if !strings.HasPrefix(matches[0], "/") {
				return "/" + matches[0], nil
			} else {
				return matches[0], nil
			}

		}
		return "", fmt.Errorf("error: Mirror link %v does not exist in repo %v", mirror.URL, mirror.RepoName)
	} else {
		// This mirror link is a residual of an old config
		return "", fmt.Errorf("error: Mirror link %v is associated with repo %v which does not exist in config", mirror.URL, mirror.RepoName)
	}
}

// Downloads the db from the mirror and adds RepoPackages
func downloadAndLoadDB(mirror MirrorDB) error {
	matches := urlRegex.FindStringSubmatch(mirror.URL)
	if len(matches) == 0 {
		return fmt.Errorf("url '%v' is invalid, does not match path regex", mirror.URL)
	}
	prefix, err := getPrefixFromMirrorDB(mirror)
	if err != nil {
		// If a mirror is invalid, don't download & load it
		return err
	}

	fileName := matches[4]
	// create directory if it does not exist
	tmpDir := path.Join(config.CacheDir, "tmp-db")
	if _, err := os.Stat(tmpDir); os.IsNotExist(err) {
		if err := os.MkdirAll(tmpDir, os.ModePerm); err != nil {
			return err
		}
	}

	filePath := filepath.Join(config.CacheDir, "tmp-db", fileName)
	ifModifiedSince := time.Time{}
	// download the db file
	if _, err := downloadFile(mirror.URL, filePath, ifModifiedSince); err != nil {
		return err
	}
	log.Printf("Extracting %v...", filePath)
	// the db file exists and have been downloaded. Now it is time to decompress it
	if err := uncompressGZ(filePath, filePath+".tar"); err != nil {
		return err
	}
	// delete the original file
	if err := os.Remove(filePath); err != nil {
		return err
	}
	log.Printf("Parsing %v...", filePath+".tar")
	fileList, err := extractFilenamesFromTar(filePath + ".tar") // file names are structured as name-version-subversionnumber
	log.Printf("Parsed %v.", filePath+".tar")
	if err != nil {
		return err
	}
	if err := os.Remove(filePath + ".tar"); err != nil {
		return err
	}
	log.Printf("Adding entries to db...")
	var repoList []RepoPackage
	for _, fileName := range fileList {
		rpkg, err := buildRepoPkg(fileName, mirror.RepoName, prefix)
		if err != nil {
			// If a repo package has an invalid name
			// e.g. is not a repo package, maybe it is a src package or whatever, we skip it
			log.Printf("error: %v\n", err)
			continue
		}
		repoList = append(repoList, rpkg)
	}
	if db := prefetchDB.Save(&repoList); db.Error != nil {
		if !strings.Contains(fmt.Sprint(db.Error), "too many SQL variables") {
			return db.Error
		}
		// Reduce the number of inserts each time
		// It is not very clear which parameter did cause "too many SQL variables". Useful reference: https://www.sqlite.org/limits.html
		maxBatchSize := 2000
		numOfPkgs := len(repoList)
		for i := 0; i < numOfPkgs; i += maxBatchSize {
			ends := i + maxBatchSize
			if ends > numOfPkgs {
				ends = numOfPkgs
			}
			newList := repoList[i:ends]
			if db := prefetchDB.Save(&newList); db.Error != nil {
				if strings.Contains(fmt.Sprint(db.Error), "too many SQL variables") {
					return fmt.Errorf("db error: Batch size is too big, change it in the config. This is a bug")
				} else {
					return db.Error
				}
			}
		}

	}
	log.Printf("Added entries to db.")
	return nil
}

// download dbs from their URLs stored in the mirror_dbs table and load their content in the repo_packages table
func downloadAndLoadDbs() error {
	mirrors := getAllMirrorsDB()
	// create tmp directory to store the db files
	dbsPath := filepath.Join(config.CacheDir, "tmp-db")
	if _, err := os.Stat(dbsPath); os.IsNotExist(err) {
		if err := os.MkdirAll(dbsPath, os.ModePerm); err != nil {
			return err
		}
	}
	for _, mirror := range mirrors {
		if err := downloadAndLoadDB(mirror); err != nil {
			// If a mirror is down or a database file is not available, we simply skip it cause
			// the cleanPrefetchDB procedure should take care of purging dead mirrors
			log.Printf("An error occurred for mirror %v :%v\n", mirror, err)
		}
	}
	return nil
}

func updateMirrorsDbs() error {
	if err := createRepoTable(); err != nil {
		return err
	}
	return downloadAndLoadDbs()
}
