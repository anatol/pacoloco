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
)

// Uncompresses a gzip file
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
	limitedReader := io.LimitReader(reader, 100*1024*1024) // Limits the size of the extracted file up to 100MB, so far community db is around 20MB
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
	if _, err = io.Copy(writer, limitedReader); err != nil {
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
func getPacolocoURL(pkg Package, prefixPath string) string {
	return strings.ReplaceAll(("/repo/" + pkg.RepoName + "/" + prefixPath + "/" + pkg.PackageName + "-" + pkg.Version + "-" + pkg.Arch), "//", "/")
}

// Builds a mirror package
// It requires the prefix, which is the relative path in which the db is contained
func buildMirrorPkg(fileName string, repoName string, prefixPath string) (MirrorPackage, error) {
	matches := filenameRegex.FindStringSubmatch(fileName)
	if len(matches) >= 7 {
		packageName := matches[1]
		version := matches[2]
		arch := matches[3]
		ext := matches[5]
		pkg := Package{PackageName: packageName, Version: version, Arch: arch, RepoName: repoName}
		pacolocoURL := getPacolocoURL(pkg, prefixPath)
		return MirrorPackage{PackageName: packageName, Version: version, Arch: arch, DownloadURL: pacolocoURL, RepoName: repoName, FileExt: ext}, nil
	}
	return MirrorPackage{}, fmt.Errorf("filename %v does not match regex, matches length is %d", fileName, len(matches))
}

// Downloads the db from the mirror and adds MirrorPackages
func downloadAndParseDb(mirror MirrorDB) error {
	// create directory if it does not exist
	tmpDir := path.Join(config.CacheDir, "tmp-db")
	if _, err := os.Stat(tmpDir); os.IsNotExist(err) {
		if err := os.MkdirAll(tmpDir, os.ModePerm); err != nil {
			return err
		}
	}
	matches := pathRegex.FindStringSubmatch(mirror.URL)
	if len(matches) == 0 {
		return fmt.Errorf("url '%v' is invalid, does not match path regex", mirror.URL)
	}
	fileName := matches[3]
	filePath := filepath.Join(tmpDir, fileName)
	// download the db file
	if err := prefetchRequest(mirror.URL, tmpDir); err != nil {
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
	var repoList []MirrorPackage
	for _, fileName := range fileList {
		rpkg, err := buildMirrorPkg(fileName, mirror.RepoName, matches[2])
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

// download dbs from their URLs stored in the mirror_dbs table and load their content in the mirror_packages table
func downloadAndParseDbs() error {
	mirrors := getAllMirrorsDB()
	// create tmp directory to store the db files
	dbsPath := filepath.Join(config.CacheDir, "tmp-db")
	if _, err := os.Stat(dbsPath); os.IsNotExist(err) {
		if err := os.MkdirAll(dbsPath, os.ModePerm); err != nil {
			return err
		}
	}
	for _, mirror := range mirrors {
		if err := downloadAndParseDb(mirror); err != nil {
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
	return downloadAndParseDbs()
}
