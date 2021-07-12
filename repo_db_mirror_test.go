package main

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// https://gist.github.com/maximilien/328c9ac19ab0a158a8df  slightly modified to create a fake package
// it expects to receive as input where to place the tarball(tarballFilePath)
// and an array of structs, where the first value is the package name, and the second one is its content
// Arch dbs are structured as packagename-version-subversion/desc , where desc contains all the relevant info
type testTarDB struct {
	PkgName string
	Content string
}

// testing content for a db
func getTestTarDB() []testTarDB {
	return []testTarDB{
		testTarDB{PkgName: "acl-2.3.1-1",
			Content: `%FILENAME%
acl-2.3.1-1-x86_64.pkg.tar.zst

%NAME%
acl

%BASE%
acl

%VERSION%
2.3.1-1

%DESC%
Access control list utilities, libraries and headers

%CSIZE%
139672

%ISIZE%
333189

%MD5SUM%
739145ae3f34b10884c678378544b10c

%SHA256SUM%
2e87a6382bcffc364015f848217d0afdcffdaa5efab43d5ee1b4d80a9645c5b8

%PGPSIG%
iHUEABYIAB0WIQQEKYl95fO9rFN6MGltQr3RFuAGjwUCYFCB0wAKCRBtQr3RFuAGj3s8AP4hGeKS33E7PGnDJVg8GGnTA6O7bTZg/wQmdNZgpMUiqAD/cjaCnHbXvciixak+KK/mA07ppArJeBo2U6WWwIPajQ0=

%URL%
https://savannah.nongnu.org/projects/acl

%LICENSE%
LGPL

%ARCH%
x86_64

%BUILDDATE%
1615888805

%PACKAGER%
Christian Hesse <arch@eworm.de>

%REPLACES%
xfsacl

%CONFLICTS%
xfsacl

%PROVIDES%
xfsacl
libacl.so=1-64

%DEPENDS%
attr
libattr.so`},
		testTarDB{PkgName: "attr-2.5.1-1",
			Content: `%FILENAME%
attr-2.5.1-1-x86_64.pkg.tar.zst

%NAME%
attr

%BASE%
attr

%VERSION%
2.5.1-1

%DESC%
Extended attribute support library for ACL support

%CSIZE%
69800

%ISIZE%
212575

%MD5SUM%
8b1373a95af2480cc778f678e540756f

%SHA256SUM%
44b400abf34e559e5c4cdd4d1cfe799795eef59780525d6d02d36a3f3152b249

%PGPSIG%
iHUEABYIAB0WIQQEKYl95fO9rFN6MGltQr3RFuAGjwUCYFCBZgAKCRBtQr3RFuAGj4MJAPoCNnY2NIrkwFDlNh75ilhhB5hrOkxuL8M7WU6nD/PZDwEAgHkq9lnFtwWxKbeS8Ojic9dQfdvPcX6uZOihFqfAMAY=

%URL%
https://savannah.nongnu.org/projects/attr

%LICENSE%
LGPL

%ARCH%
x86_64

%BUILDDATE%
1615888678

%PACKAGER%
Christian Hesse <arch@eworm.de>

%REPLACES%
xfsattr

%CONFLICTS%
xfsattr

%PROVIDES%
xfsattr
libattr.so=1-64

%DEPENDS%
glibc

%MAKEDEPENDS%
gettext

`}}
}

// creates a test tar file
func createDbTarball(tarballFilePath string, content []testTarDB) {
	file, err := os.Create(tarballFilePath)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	gzipWriter := gzip.NewWriter(file)
	defer gzipWriter.Close()

	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	for _, t := range content {
		pkgName := t.PkgName
		content := t.Content
		addFileToTarWriter(pkgName, content, tarWriter)
	}
}

// adds a file to the tar under pkgname/desc
func addFileToTarWriter(pkgName string, content string, tarWriter *tar.Writer) {

	header := &tar.Header{
		Name: path.Join(pkgName, "desc"),
		Size: int64(len(content)),
	}

	if err := tarWriter.WriteHeader(header); err != nil {
		log.Fatal(err)
	}
	if _, err := io.Copy(tarWriter, strings.NewReader(content)); err != nil {
		log.Fatal(err)
	}
}

// Uncompresses a gzip file
// TODO set some limits to avoid OOM with gzip bombs in uncompressGZ.
func TestUncompressGZ(t *testing.T) {
	err := uncompressGZ("nope", "nope")
	tmpDir := testSetupHelper(t)
	if err == nil {
		t.Errorf("Should raise an error")
	}
	filePath := path.Join(tmpDir, "test.gz")
	testString := ``
	gzipfile, err := os.Create(filePath)
	if err != nil {
		log.Fatal(err)
	}
	writer := gzip.NewWriter(gzipfile)
	reader := strings.NewReader(testString)
	if _, err = io.Copy(writer, reader); err != nil {
		log.Fatal(err)
	}
	writer.Close()
	if err = uncompressGZ(filePath, filePath+".uncompressed"); err != nil {
		log.Fatal(err)
	}
	byteStr, err := ioutil.ReadFile(filePath + ".uncompressed")
	if string(byteStr) != testString {
		t.Errorf("Expected %v, got %v ", testString, string(byteStr))
	}
	if err != nil {
		log.Fatal(err)
	}
}
func TestExtractFilenamesFromTar(t *testing.T) {
	tmpDir := testSetupHelper(t)
	filePath := path.Join(tmpDir, "test.gz")
	testString := ``
	gzipfile, err := os.Create(filePath)
	if err != nil {
		log.Fatal(err)
	}
	writer := gzip.NewWriter(gzipfile)
	reader := strings.NewReader(testString)
	if _, err = io.Copy(writer, reader); err != nil {
		log.Fatal(err)
	}
	writer.Close()
	if _, err = extractFilenamesFromTar("nope"); err == nil {
		log.Fatal("Should raise an error")
	}
	// now create a valid db file
	filePath = path.Join(tmpDir, "core.db")
	createDbTarball(filePath, getTestTarDB())
	if err = uncompressGZ(filePath, filePath+".uncompressed"); err != nil {
		log.Fatal(err)
	}
	got, err := extractFilenamesFromTar(filePath + ".uncompressed")
	if err != nil {
		log.Fatal(err)
	}
	want := []string{"acl-2.3.1-1-x86_64.pkg.tar.zst", "attr-2.5.1-1-x86_64.pkg.tar.zst"}
	if !cmp.Equal(got, want) {
		log.Fatalf("Want %v, got %v", want, got)
	}
}

func TestGetPacolocoURL(t *testing.T) {
	// create a package
	got := getPacolocoURL(Package{PackageName: "webkit2gtk", RepoName: "testRepo", Version: "2.26.4-1", Arch: "x86_64"})
	want := "/repo/testRepo/webkit2gtk-2.26.4-1-x86_64"
	if got != want {
		t.Errorf("Want %v, got %v", want, got)
	}
}

func TestBuildRepoPkg(t *testing.T) {
	got, err := buildRepoPkg("libstdc++5-3.3.6-7-x86_64.pkg.tar.zst", "testRepo")
	if err != nil {
		log.Fatal(err)
	}
	want := RepoPackage{PackageName: "libstdc++5", RepoName: "testRepo", Version: "3.3.6-7", Arch: "x86_64", DownloadURL: "/repo/testRepo/libstdc++5-3.3.6-7-x86_64"}
	if !cmp.Equal(got, want) {
		t.Errorf("Got %v, want %v", got, want)
	}
	if _, err = buildRepoPkg("webkit2gtk-2.26.4-1-x86_6-4.pkg.tar.zst", "testRepo"); err == nil {
		t.Errorf("Should have thrown an error cause the string is invalid")
	}
}

/*
func TestDownloadAndLoadDB(t *testing.T) {
	// tested in prefetch integration tests

}

func TestDownloadAndLoadDbs(t *testing.T) {
	// tested in prefetch integration tests
}

func TestUpdateMirrorsDbs(t *testing.T) {
	// tested in prefetch integration tests
}*/
