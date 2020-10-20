package main

import (
	"log"
	"os/user"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

const DefaultPort = 9129
const DefaultCacheDir = "/var/cache/pacoloco"

type Repo struct {
	Url  string   `yaml:"url"`
	Urls []string `yaml:"urls"`
}

type Config struct {
	CacheDir        string          `yaml:"cache_dir"`
	Port            int             `yaml:"port"`
	Repos           map[string]Repo `yaml:"repos,omitempty"`
	PurgeFilesAfter int             `yaml:"purge_files_after"`
	DownloadTimeout int             `yaml:"download_timeout"`
}

var config *Config

func parseConfig(raw []byte) *Config {
	var result = &Config{
		CacheDir: DefaultCacheDir,
		Port:     DefaultPort,
	}

	if err := yaml.Unmarshal(raw, &result); err != nil {
		log.Fatal(err)
	}

	// validate config
	for name, repo := range result.Repos {
		if repo.Url != "" && len(repo.Urls) > 0 {
			log.Fatalf("repo '%v' specifies both url and urls parameters, please use only one of them", name)
		}
		if repo.Url == "" && len(repo.Urls) == 0 {
			log.Fatalf("please specify url for repo '%v'", name)
		}
	}

	if result.PurgeFilesAfter < 10*60 && result.PurgeFilesAfter != 0 {
		log.Fatalf("purge_files_after period is too low (%v) please specify at least 10 minutes", result.PurgeFilesAfter)
	}

	if unix.Access(result.CacheDir, unix.R_OK|unix.W_OK) != nil {
		u, err := user.Current()
		if err != nil {
			log.Fatal(err)
		}
		log.Fatalf("directory %v does not exist or isn't writable for user %v", result.CacheDir, u.Username)
	}

	return result
}
