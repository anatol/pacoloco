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
			pkgName = matches[1]
			pkgList = append(pkgList, pkgName)
		}
	}
	return pkgList, nil
}

// This function returns a url which should download the exactly identical pkg when sent to pacoloco except for the file extension
func getPacolocoURL(pkg Package) string {
	return "/repo/" + pkg.RepoName + "/" + pkg.PackageName + "-" + pkg.Version + "-" + pkg.Arch
}

// Builds a repository package
func buildRepoPkg(fileName string, repoName string) (RepoPackage, error) {
	matches := filenameRegex.FindStringSubmatch(fileName)
	if len(matches) >= 7 {
		packageName := matches[1]
		version := matches[2]
		arch := matches[3]
		pkg := Package{PackageName: packageName, Version: version, Arch: arch, RepoName: repoName}
		pacolocoURL := getPacolocoURL(pkg)
		return RepoPackage{PackageName: packageName, Version: version, Arch: arch, DownloadURL: pacolocoURL, RepoName: repoName}, nil
	}
	return RepoPackage{}, fmt.Errorf("filename %v does not match regex, matches length is %d", fileName, len(matches))
}

// Downloads the db from the mirror and adds RepoPackages
func downloadAndLoadDB(mirror MirrorDB) error {
	matches := urlRegex.FindStringSubmatch(mirror.URL)
	if len(matches) == 0 {
		return fmt.Errorf("url '%v' is invalid, does not match path regex", mirror.URL)
	}

	fileName := matches[4]
	// create directory if it does not exist
	tmpDir := path.Join(config.CacheDir, "tmp-db")
	if _, err := os.Stat(tmpDir); os.IsNotExist(err) {
		if err := os.MkdirAll(tmpDir, os.ModePerm); err != nil {
			log.Printf("error: %v\n", err)
			return err
		}
	}

	filePath := filepath.Join(config.CacheDir, "tmp-db", fileName)
	ifModifiedSince := time.Time{}
	// download the db file
	if _, err := downloadFile(mirror.URL, filePath, ifModifiedSince); err != nil {
		return err
	} else {
		// the db file exists and have been downloaded. Now it is time to decompress it
		uncompressGZ(filePath, filePath+".tar")
		os.Remove(filePath)                                         // delete the original file
		fileList, err := extractFilenamesFromTar(filePath + ".tar") // file names are structured as name-version-subversionnumber
		if err != nil {
			log.Printf("error: %v\n", err)
			return err
		}
		os.Remove(filePath + ".tar")
		for _, fileName := range fileList {
			rpkg, err := buildRepoPkg(fileName, mirror.RepoName)
			if err != nil {
				// If a repo package has an invalid name
				// e.g. is not a repo package, maybe it is a src package or whatever, we skip it
				log.Printf("error: %v\n", err)
				continue
			}
			prefetchDB.Save(rpkg)
		}
		return nil
	}
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

func updateMirrorsDbs() {
	createRepoTable()
	if err := downloadAndLoadDbs(); err != nil {
		log.Printf("An error occurred while downloading db files: %v", err)
	}
}
