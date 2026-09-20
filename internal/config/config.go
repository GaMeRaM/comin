package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/nlewo/comin/internal/types"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

func Read(path string) (config types.Configuration, err error) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close() // nolint

	d := yaml.NewDecoder(file)
	if err := d.Decode(&config); err != nil {
		return config, err
	}
	for i, remote := range config.Remotes {
		if remote.Auth.AccessTokenPath != "" {
			content, err := os.ReadFile(remote.Auth.AccessTokenPath)
			if err != nil {
				return config, err
			}
			config.Remotes[i].Auth.AccessToken = strings.TrimSpace(string(content))
		}
		// On GitLab and GitHub, any non blank username is working
		if remote.Auth.Username == "" {
			config.Remotes[i].Auth.Username = "comin"
		}
		if remote.Timeout == 0 {
			config.Remotes[i].Timeout = 300
		}
		if remote.Branches.Main.Operation == "" {
			config.Remotes[i].Branches.Main.Operation = "switch"
		}
		if remote.Branches.Testing.Operation == "" {
			config.Remotes[i].Branches.Testing.Operation = "test"
		}

	}

	if n := config.Niks3; n != nil {
		if runtime.GOOS != "linux" {
			return config, fmt.Errorf("niks3 currently supports NixOS only")
		}
		if len(config.Remotes) != 0 {
			return config, fmt.Errorf("choose either niks3 or Git remotes")
		}
		u, err := url.Parse(n.URL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
			return config, fmt.Errorf("niks3.url must be an HTTP(S) pin URL")
		}
		if n.NetrcFile != "" && u.Scheme != "https" {
			return config, fmt.Errorf("niks3.netrc_file requires an HTTPS pin URL")
		}
		if n.Timeout == 0 {
			n.Timeout = 10
		}
		if n.Poller.Period == 0 {
			n.Poller.Period = 60
		}
		if n.Timeout < 0 || n.Poller.Period < 0 {
			return config, fmt.Errorf("niks3 timeout and poll period must be positive")
		}
		if n.Operation == "" {
			n.Operation = "switch"
		}
		if !slices.Contains([]string{"switch", "boot", "test"}, n.Operation) {
			return config, fmt.Errorf("invalid niks3 operation %q", n.Operation)
		}
	}
	if config.ApiServer.ListenAddress == "" {
		config.ApiServer.ListenAddress = "127.0.0.1"
	}
	if config.ApiServer.Port == 0 {
		config.ApiServer.Port = 4242
	}
	if config.Exporter.ListenAddress == "" {
		config.Exporter.ListenAddress = "0.0.0.0"
	}
	if config.Exporter.Port == 0 {
		config.Exporter.Port = 4243
	}
	if config.StateFilepath == "" {
		config.StateFilepath = filepath.Join(config.StateDir, "state.json")
	}
	if config.RepositorySubdir == "" {
		config.RepositorySubdir = "."
	}
	supportedRepositoryTypes := []string{"flake", "nix"}
	if config.Niks3 == nil && !slices.Contains(supportedRepositoryTypes, config.RepositoryType) {
		return config, fmt.Errorf("config: repository type is '%s' while it be one of '%s'", config.RepositoryType, supportedRepositoryTypes)
	}
	if config.Grpc.UnixSocketPath == "" {
		config.Grpc.UnixSocketPath = filepath.Join(config.StateDir, "grpc.sock")
	}
	if config.EvalTimeout == 0 {
		config.EvalTimeout = 1800
	}
	if config.BuildTimeout == 0 {
		config.BuildTimeout = 1800
	}
	logrus.Debugf("Config is '%#v'", config)
	return
}

func MkGitConfig(config types.Configuration) types.GitConfig {
	return types.GitConfig{
		Path:                  filepath.Join(config.StateDir, "repository"),
		Dir:                   config.RepositorySubdir,
		Remotes:               config.Remotes,
		GpgPublicKeyPaths:     config.GpgPublicKeyPaths,
		SshAllowedSignersPath: config.SshAllowedSignersPath,
		Submodules:            config.Submodules,
	}
}
