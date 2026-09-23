package types

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

type Niks3Fetcher struct {
	URL                string `yaml:"url"`
	TestingURL         string `yaml:"testing_url"`
	TestingOperation   string `yaml:"testing_operation"`
	NetrcFile          string `yaml:"netrc_file"`
	AWSCredentialsFile string `yaml:"aws_credentials_file"`
	Timeout            int    `yaml:"timeout"`
	Poller             Poller `yaml:"poller"`
	ManualFetch        bool   `yaml:"manual_fetch"`
	Operation          string `yaml:"operation"`
}

// Testing pins are scoped to the exact main output, so an older test can never
// mask a newly published main. Both objects keep niks3's plain store-path format.
func (n Niks3Fetcher) TestingPin(main string) (Niks3Fetcher, error) {
	if err := ValidateNiks3Path(main); err != nil {
		return n, err
	}
	u, err := url.Parse(n.TestingURL)
	if err != nil {
		return n, err
	}
	u.Path += "-" + strings.Split(strings.TrimPrefix(main, "/nix/store/"), "-")[0]
	n.URL = u.String()
	_, err = n.ParseURL()
	return n, err
}

// ParseURL accepts the same endpoint/profile/region query parameters as Nix's
// S3 substituter. Credentials belong in a runtime file, never in the URL.
func (n Niks3Fetcher) ParseURL() (*url.URL, error) {
	u, err := url.Parse(n.URL)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("niks3.url must be an HTTP(S) or S3 pin URL without embedded credentials")
	}
	if n.NetrcFile != "" && u.Scheme != "https" {
		return nil, fmt.Errorf("niks3.netrc_file requires HTTPS")
	}
	if u.Scheme != "s3" {
		if (u.Scheme != "http" && u.Scheme != "https") || n.AWSCredentialsFile != "" {
			return nil, fmt.Errorf("AWS credentials require an S3 pin URL")
		}
		return u, nil
	}
	if n.AWSCredentialsFile == "" {
		return nil, fmt.Errorf("private S3 pins require aws_credentials_file")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("invalid S3 pin query")
	}
	for key, values := range query {
		if len(values) != 1 || (key != "endpoint" && key != "scheme" && key != "region" && key != "profile") {
			return nil, fmt.Errorf("unsupported or repeated S3 pin parameter")
		}
	}
	if scheme := query.Get("scheme"); scheme != "" && scheme != "https" {
		return nil, fmt.Errorf("private S3 pins require HTTPS")
	}
	endpoint, err := url.Parse("https://" + query.Get("endpoint"))
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, fmt.Errorf("S3 endpoint must be a host with an optional port")
	}
	if !niks3PinKey.MatchString(u.Path) || u.Path == "/pins/." || u.Path == "/pins/.." {
		return nil, fmt.Errorf("S3 pin URL must address pins/<name>")
	}
	return u, nil
}

var niks3PinKey = regexp.MustCompile(`^/pins/[a-zA-Z0-9._-]{1,256}$`)

// Only a top-level output path is accepted: no derivations, subpaths or
// command-line options. Realization must never turn into a local build.
var niks3Path = regexp.MustCompile(`^/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-[a-zA-Z0-9+._?=-]+$`)

func ValidateNiks3Path(path string) error {
	if !niks3Path.MatchString(path) || strings.HasSuffix(path, ".drv") {
		return fmt.Errorf("pin must contain one Nix store output path")
	}
	return nil
}
