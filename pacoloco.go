package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gorm.io/gorm"
)

var configFile = flag.String("config", "/etc/pacoloco.yaml", "Path to config file")

var (
	pathRegex       *regexp.Regexp
	filenameRegex   *regexp.Regexp // to get the details of a package (arch, version etc)
	filenameDBRegex *regexp.Regexp // to get the filename from the db file
	mirrorlistRegex *regexp.Regexp // to extract the url from a mirrorlist file
	prefetchDB      *gorm.DB
)

// Accepted formats
var allowedPackagesExtensions []string

func init() {
	var err error
	pathRegex, err = regexp.Compile("^/repo/([^/]*)(/.*)?/([^/]*)$")
	if err != nil {
		panic(err)
	}
	// source: https://archlinux.org/pacman/makepkg.conf.5.html PKGEXT section, sorted with compressed formats as first.
	allowedPackagesExtensions = []string{".pkg.tar.zst", ".pkg.tar.gz", ".pkg.tar.xz", ".pkg.tar.bz2", ".pkg.tar.lzo", ".pkg.tar.lrz", ".pkg.tar.lz4", ".pkg.tar.lz", ".pkg.tar.Z", ".pkg.tar"}

	// Filename regex explanation (also here https://regex101.com/r/qB0fQ7/36 )
	/*
		The filename relevant matches are:
		^([a-z0-9._+-]+)			a package filename must be a combination of lowercase letters,numbers,dots, underscores, plus symbols or dashes
		-							separator
		([a-z0-9A-Z:._+]+-[0-9.]+)	epoch/version. an epoch can be written as (whatever)-(sequence of numbers with possibly dots)
		-							separator
		([a-zA-Z0-9:._+]+)			arch
		-							separator
		(([.]...)$					file extension, explanation below

			File extension explanation:
			(
				([.]pkg[.]tar		final file extension must start with .pkg.tar, then another suffix can be present
					(
						([.]gz)|	they are in disjunction with each other
						([.]bz2)|
						([.]xz)|
						([.]zst)|
						([.]lzo)|
						([.]lrz)|
						([.]lz4)|
						([.]lz)|
						([.]Z)
					)?				they are not mandatory
				)
				([.]sig)?			It could be a signature, so it could have a terminating .sig extension
			)$


	*/
	filenameRegex, err = regexp.Compile("^([a-z0-9._+-]+)-([a-zA-Z0-9:._+]+-[0-9.]+)-([a-zA-Z0-9:._+]+)(([.]pkg[.]tar(([.]gz)|([.]bz2)|([.]xz)|([.]zst)|([.]lzo)|([.]lrz)|([.]lz4)|([.]lz)|([.]Z))?)([.]sig)?)$")
	if err != nil {
		log.Fatal(err)
	} // shouldn't happen
	filenameDBRegex, err = regexp.Compile("[%]FILENAME[%]\n([^\n]+)\n")
	if err != nil {
		log.Fatal(err)
	} // shouldn't happen

	//	Analysis of the mirrorlistRegex regex (also here https://regex101.com/r/1oEit0/1):
	//  ^\s*Server\s*=\s*						Starts with `Server=` keyword, with optional spaces before and after `Server` and `=`
	//  ([^\s$]+)(\$[^\s]+)						Non white spaces and not $ characters composes the url, which must end with a $ string (e.g. `$repo/os/$arch`)
	//  [\s]*									Optional ending whitespaces
	//  (#.*)?									Optional comment starting with #

	mirrorlistRegex, err = regexp.Compile(`^\s*Server\s*=\s*([^\s$]+)(\$[^\s]+)[\s]*(#.*)?$`)
	if err != nil {
		log.Fatal(err)
	} // shouldn't happen
}

func main() {
	flag.Parse()
	log.SetFlags(log.Lshortfile)

	log.Print("Reading config file from ", *configFile)
	yaml, err := os.ReadFile(*configFile)
	if err != nil {
		log.Fatal(err)
	}
	config = parseConfig(yaml)
	if config.LogTimestamp == true {
		log.SetFlags(log.LstdFlags)
	}
	if config.Prefetch != nil {
		prefetchTicker := setupPrefetchTicker()
		defer prefetchTicker.Stop()
		setupPrefetch() // enable refresh
	}

	for repoName := range config.Repos {
		cachePath := filepath.Join(config.CacheDir, "pkgs", repoName)
		totalCacheSize, totalPackageCount, err := gatherCacheStats(cachePath)
		if err != nil {
			log.Println("Gathering size failed for ", repoName)
		}
		cacheSizeGauge.WithLabelValues(repoName).Set(totalCacheSize)
		cachePackageGauge.WithLabelValues(repoName).Set(totalPackageCount)
	}

	if config.PurgeFilesAfter != 0 {
		cleanupTicker := setupPurgeStaleFilesRoutine()
		defer cleanupTicker.Stop()
	}

	if config.HttpProxy != "" {
		proxyUrl, err := url.Parse(config.HttpProxy)
		if err != nil {
			log.Fatal(err)
		}
		http.DefaultTransport = &http.Transport{Proxy: http.ProxyURL(proxyUrl)}
	}

	if config.UserAgent == "" {
		config.UserAgent = "Pacoloco/1.2"
	}

	listenAddr := fmt.Sprintf("%s:%d", config.Address, config.Port)
	log.Printf("Starting server at address %s:%d", config.Address, config.Port)
	// The request path looks like '/repo/$reponame/$pathatmirror'
	http.HandleFunc("/repo/", pacolocoHandler)
	// Expose prometheus metrics
	http.Handle("/metrics", promhttp.Handler())
	log.Fatal(http.ListenAndServe(listenAddr, nil))
}

// walks through given directory and gathers its stats. Returns cache size in bytes and package count
func gatherCacheStats(repoDir string) (totalCacheSize float64, totalPackageCount float64, err error) {
	var size int64
	var numberOfPackages int64
	err = filepath.Walk(repoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
			numberOfPackages++
		}
		return err
	})
	return float64(size), float64(numberOfPackages), err
}

func pacolocoHandler(w http.ResponseWriter, req *http.Request) {
	if err := handleRequest(w, req); err != nil {
		log.Println(err)
		w.WriteHeader(http.StatusNotFound)
	}
}

var (
	cacheRequestsCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pacoloco_cache_requests_total",
		Help: "Number of requests to cache",
	}, []string{"repo"})
	cacheServedCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pacoloco_cache_hits_total",
		Help: "The total number of cache hits",
	}, []string{"repo"})
	cacheMissedCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pacoloco_cache_miss_total",
		Help: "The total number of cache misses",
	}, []string{"repo"})
	cacheServingFailedCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pacoloco_cache_errors_total",
		Help: "Number of errors while trying to serve cached file",
	}, []string{"repo"})

	cacheSizeGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "pacoloco_cache_size_bytes",
		Help: "Number of bytes taken by the cache",
	}, []string{"repo"})
	cachePackageGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "pacoloco_cache_packages_total",
		Help: "Number of packages in the cache",
	}, []string{"repo"})

	// Track individual mirror behavior
	downloadedFilesCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pacoloco_downloaded_files_total",
		Help: "Total number of downloaded files",
	}, []string{"repo", "upstream", "status"})
)

// force resources prefetching
func prefetchRequest(urlPath string, cachePath string) error {
	f, err := parseRequestURL(urlPath)
	if err != nil {
		return err
	}

	if f.getRepo() == nil {
		return fmt.Errorf("cannot find repo %s in the config file", f.repoName)
	}
	if cachePath == "" {
		// use default cache path
		if err := f.mkCacheDir(); err != nil {
			return err
		}
	} else {
		f.cacheDir = cachePath
		f.cachedFilePath = filepath.Join(cachePath, f.fileName)
	}

	d, err := getDownloader(f)
	if err != nil {
		return err
	}
	if d != nil {
		err := d.waitForCompletion()
		d.decrementUsage()
		if err != nil {
			return err
		}
	}

	if config.Prefetch != nil {
		if !strings.HasSuffix(f.fileName, ".sig") && !strings.HasSuffix(f.fileName, ".db") {
			updateDBRequestedFile(f.repoName, f.fileName) // update info for prefetching
		} else if strings.HasSuffix(f.fileName, ".db") {
			updateDBRequestedDB(f.repoName, f.pathAtRepo, f.fileName)
		}
	}

	return nil
}

func handleRequest(w http.ResponseWriter, req *http.Request) error {
	f, err := parseRequestURL(req.URL.Path)
	if err != nil {
		return err
	}

	if f.getRepo() == nil {
		return fmt.Errorf("cannot find repo %s in the config file", f.repoName)
	}

	cacheRequestsCounter.WithLabelValues(f.repoName).Inc()

	// create cache directory if needed
	if err := f.mkCacheDir(); err != nil {
		return err
	}

	modTime, r, err := getDownloadReader(f)
	if err != nil {
		cacheServingFailedCounter.WithLabelValues(f.repoName).Inc()
		return err
	}
	if r == nil {
		log.Printf("serving cached file for %v", f.key())
		http.ServeFile(w, req, f.cachedFilePath)
		cacheServedCounter.WithLabelValues(f.repoName).Inc()
	} else {
		http.ServeContent(w, req, f.fileName, modTime, r)
		cacheMissedCounter.WithLabelValues(f.repoName).Inc()
		if err := r.Close(); err != nil {
			return err
		}
	}

	if config.Prefetch != nil {
		if !strings.HasSuffix(f.fileName, ".sig") && !strings.HasSuffix(f.fileName, ".db") {
			updateDBRequestedFile(f.repoName, f.fileName) // update info for prefetching
		} else if strings.HasSuffix(f.fileName, ".db") {
			updateDBRequestedDB(f.repoName, f.pathAtRepo, f.fileName)
		}
	}

	return nil
}
