package main

import (
	"log"
	"os/user"

	"github.com/gorhill/cronexpr"
	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

const DefaultPort = 9129
const DefaultCacheDir = "/var/cache/pacoloco"
const DefaultTTLUnaccessed = 30
const DefaultTTLUnupdated = 200
const DefaultDBName = "sqlite-pkg-cache.db"

type Repo struct {
	URL        string   `yaml:"url"`
	URLs       []string `yaml:"urls"`
	Mirrorlist string   `yaml:"mirrorlist"`
}

type RefreshPeriod struct {
	Cron          string `yaml:"cron"`
	TTLUnaccessed int    `yaml:"ttl_unaccessed_in_days"`
	TTLUnupdated  int    `yaml:"ttl_unupdated_in_days"`
}

type Config struct {
	CacheDir        string          `yaml:"cache_dir"`
	Port            int             `yaml:"port"`
	Repos           map[string]Repo `yaml:"repos,omitempty"`
	PurgeFilesAfter int             `yaml:"purge_files_after"`
	DownloadTimeout int             `yaml:"download_timeout"`
	Prefetch        *RefreshPeriod  `yaml:"prefetch"`
	HttpProxy       string          `yaml:"http_proxy"`
}

var config *Config

func parseConfig(raw []byte) *Config {
	var result = Config{
		CacheDir: DefaultCacheDir,
		Port:     DefaultPort,
		Prefetch: nil,
	}

	if err := yaml.Unmarshal(raw, &result); err != nil {
		log.Fatal(err)
	}

	// validate config
	for name, repo := range result.Repos {
		if repo.URL != "" && len(repo.URLs) > 0 {
			log.Fatalf("repo '%v' specifies both url and urls parameters, please use only one of them", name)
		}
		if repo.URL != "" && repo.Mirrorlist != "" {
			log.Fatalf("repo '%v' specifies both url and mirrorlist parameter, please use only one of them", name)
		}
		if len(repo.URLs) > 0 && repo.Mirrorlist != "" {
			log.Fatalf("repo '%v' specifies both urls and mirrorlist parameter, please use only one of them", name)
		}
		if repo.URL == "" && len(repo.URLs) == 0 && repo.Mirrorlist == "" {
			log.Fatalf("please specify url(s) or mirrorlist for repo '%v'", name)
		}
		// validate Mirrorlist config
		if repo.Mirrorlist != "" && unix.Access(repo.Mirrorlist, unix.R_OK) != nil {
			u, err := user.Current()
			if err != nil {
				log.Fatal(err)
			}
			log.Fatalf("mirrorlist file %v for repo %v does not exist or isn't readable for user %v", repo.Mirrorlist, name, u.Username)
		}
	}

	if result.PurgeFilesAfter < 10*60 && result.PurgeFilesAfter != 0 {
		log.Fatalf("'purge_files_after' period is too low (%v) please specify at least 10 minutes", result.PurgeFilesAfter)
	}

	if unix.Access(result.CacheDir, unix.R_OK|unix.W_OK) != nil {
		u, err := user.Current()
		if err != nil {
			log.Fatal(err)
		}
		log.Fatalf("directory %v does not exist or isn't writable for user %v", result.CacheDir, u.Username)
	}
	// validate Prefetch config

	if result.Prefetch != nil {

		// set default values
		if result.Prefetch.TTLUnaccessed == 0 {
			result.Prefetch.TTLUnaccessed = DefaultTTLUnaccessed
		}
		if result.Prefetch.TTLUnupdated == 0 {
			result.Prefetch.TTLUnupdated = DefaultTTLUnupdated
		}
		// check Prefetch config
		if result.Prefetch.TTLUnaccessed < 0 {
			log.Fatal("'ttl_unaccessed_in_days' value is too low. Please set it to a value greater than 0")
		}
		if result.Prefetch.TTLUnupdated < 0 {
			log.Fatal("'ttl_unupdated_in_days' value is too low. Please set it to a value greater than 0")
		}
		if _, err := cronexpr.Parse(result.Prefetch.Cron); err != nil {
			log.Fatal("Invalid cron string (if you don't know how to compose them, there are many online utilities for doing so). Please check https://github.com/gorhill/cronexpr#implementation for documentation.")
		}
	}
	return &result
}
