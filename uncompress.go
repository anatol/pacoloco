package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// Limits the size of the extracted file up to 100MB, so far community db is around 20MB
const databaseSizeLimit = 100 * 1024 * 1024

type decompressFunc func(r io.Reader) (io.Reader, error)

var decompressors = []struct {
	magic []byte
	f     decompressFunc
}{{
	gzipMagic, gzipReader,
}, {
	xzMagic, xzReader,
}, {
	zstdMagic, zstdReader,
},
}

// https://socketloop.com/tutorials/golang-gunzip-file
var gzipMagic = []byte{0x1f, 0x8b}

func gzipReader(r io.Reader) (io.Reader, error) {
	return gzip.NewReader(r)
}

// reference https://tukaani.org/xz/xz-file-format-1.0.4.txt
var xzMagic = []byte{0xFD, 0x37, 0x7A, 0x58, 0x5A, 0x00}

func xzReader(r io.Reader) (io.Reader, error) {
	return xz.NewReader(r)
}

// reference: https://www.rfc-editor.org/rfc/rfc8878.html
var zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

func zstdReader(r io.Reader) (io.Reader, error) {
	return zstd.NewReader(r)
}

func uncompress(inputFile string, targetFile string) error {
	compressedFile, err := os.Open(inputFile)
	if err != nil {
		return err
	}
	defer compressedFile.Close()

	fi, err := compressedFile.Stat()
	if err != nil {
		return fmt.Errorf("%s: unable to get file size", inputFile)
	}
	magicSize := min(fi.Size(), 6)
	magic := make([]byte, magicSize)
	_, err = compressedFile.ReadAt(magic, 0)
	if err != nil {
		return fmt.Errorf("%s unable to read header", inputFile)
	}
	_, err = compressedFile.Seek(0, 0)
	if err != nil {
		return err
	}

	var f decompressFunc

	for _, d := range decompressors {
		l := min(len(d.magic), int(magicSize))
		if bytes.Equal(d.magic, magic[:l]) {
			f = d.f
			break
		}
	}
	if f == nil {
		return fmt.Errorf("%s: unknown database compression format", inputFile)
	}

	reader, err := f(compressedFile)
	if err != nil {
		return err
	}
	limitedReader := io.LimitReader(reader, databaseSizeLimit)
	writer, err := os.Create(targetFile)
	if err != nil {
		return err
	}
	defer writer.Close()
	if _, err = io.Copy(writer, limitedReader); err != nil {
		return err
	}
	return nil
}
