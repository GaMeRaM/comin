package types

import (
	"fmt"
	"regexp"
	"strings"
)

type Niks3Fetcher struct {
	URL       string `yaml:"url"`
	Timeout   int    `yaml:"timeout"`
	Poller    Poller `yaml:"poller"`
	Operation string `yaml:"operation"`
}

// Only a top-level output path is accepted: no derivations, subpaths or
// command-line options. Realization must never turn into a local build.
var niks3Path = regexp.MustCompile(`^/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-[a-zA-Z0-9+._?=-]+$`)

func ValidateNiks3Path(path string) error {
	if !niks3Path.MatchString(path) || strings.HasSuffix(path, ".drv") {
		return fmt.Errorf("pin must contain one Nix store output path")
	}
	return nil
}
