package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gorm.io/gorm"
)

var configFile = flag.String("config", "/etc/pacoloco.yaml", "Path to config file")

var (
	pathRegex       = regexp.MustCompile("^/repo/([^/]*)(/.*)?/([^/]*)$")
	filenameRegex   = regexp.MustCompile("^([a-z0-9._+-]+)-([a-zA-Z0-9:._+]+-[0-9.]+)-([a-zA-Z0-9:._+]+)(([.]pkg[.]tar(([.]gz)|([.]bz2)|([.]xz)|([.]zst)|([.]lzo)|([.]lrz)|([.]lz4)|([.]lz)|([.]Z))?)([.]sig)?)$")
	filenameDBRegex = regexp.MustCompile("[%]FILENAME[%]\n([^\n]+)\n")
	mirrorlistRegex = regexp.MustCompile(`^\s*Server\s*=\s*([^\s$]+)(\$[^\s]+)[\s]*(#.*)?$`)
	prefetchDB      *gorm.DB
)

// source: https://archlinux.org/pacman/makepkg.conf.5.html PKGEXT section, sorted with compressed formats as first.
var allowedPackagesExtensions = []string{".pkg.tar.zst", ".pkg.tar.gz", ".pkg.tar.xz", ".pkg.tar.bz2", ".pkg.tar.lzo", ".pkg.tar.lrz", ".pkg.tar.lz4", ".pkg.tar.lz", ".pkg.tar.Z", ".pkg.tar"}

func main() {
	flag.Parse()
	log.SetFlags(log.Lshortfile)

	log.Print("Reading config file from ", *configFile)
	yaml, err := os.ReadFile(*configFile)
	if err != nil {
		log.Fatal(err)
	}
	config, err = parseConfig(yaml)
	if err != nil {
		log.Fatal(err)
	}
	if config.LogTimestamp {
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
	// ReadHeaderTimeout protects against clients that open a connection and
	// never send a request (slowloris); IdleTimeout reclaims parked
	// keep-alive connections. Deliberately no ReadTimeout/WriteTimeout:
	// serving a large package to a slow client is a legitimate long-running
	// response.
	server := &http.Server{
		Addr:              listenAddr,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	if config.Tls != nil {
		err = server.ListenAndServeTLS(config.Tls.Certificate, config.Tls.Key)
	} else {
		err = server.ListenAndServe()
	}
	if err != nil {
		log.Fatal(err)
	}
}

// walks through given directory and gathers its stats. Returns cache size in bytes and package count
func gatherCacheStats(repoDir string) (totalCacheSize float64, totalPackageCount float64, err error) {
	var size int64
	var numberOfPackages int64
	err = filepath.WalkDir(repoDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			size += info.Size()
			numberOfPackages++
		}
		return nil
	})
	return float64(size), float64(numberOfPackages), err
}

// errNotFound marks request errors that must surface as 404: malformed
// request paths and repos missing from the config. Everything else (upstream
// failures, disk errors) is a server-side problem and must not masquerade as
// "no such file", both for pacman and for whoever reads the logs.
var errNotFound = errors.New("not found")

func pacolocoHandler(w http.ResponseWriter, req *http.Request) {
	if err := handleRequest(w, req); err != nil {
		log.Println(err)
		if errors.Is(err, errNotFound) {
			w.WriteHeader(http.StatusNotFound)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
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

	maybeUpdatePrefetchDB(f)
	return nil
}

func handleRequest(w http.ResponseWriter, req *http.Request) error {
	f, err := parseRequestURL(req.URL.Path)
	if err != nil {
		return fmt.Errorf("%w: %v", errNotFound, err)
	}

	if f.getRepo() == nil {
		return fmt.Errorf("%w: cannot find repo %s in the config file", errNotFound, f.repoName)
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
		// ServeContent has already written the response; returning an
		// error here would only produce a superfluous WriteHeader call.
		if err := r.Close(); err != nil {
			log.Printf("error closing download reader for %v: %v", f.key(), err)
		}
	}

	maybeUpdatePrefetchDB(f)
	return nil
}

func maybeUpdatePrefetchDB(f *RequestedFile) {
	if config.Prefetch == nil {
		return
	}
	if !strings.HasSuffix(f.fileName, ".sig") && !strings.HasSuffix(f.fileName, ".db") {
		updateDBRequestedFile(f.repoName, f.fileName)
	} else if strings.HasSuffix(f.fileName, ".db") {
		updateDBRequestedDB(f.repoName, f.pathAtRepo, f.fileName)
	}
}
