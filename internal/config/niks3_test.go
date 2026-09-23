package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNiks3Config(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		valid        bool
	}{
		{"defaults", "niks3:\n  url: https://cache.example/pins/device", true},
		{"manual download", "niks3:\n  url: https://cache.example/pins/device\n  manual_download: true", true},
		{"HTTPS authentication", "niks3:\n  url: https://cache.example/pins/device\n  netrc_file: /run/secrets/cache.netrc", true},
		{"private S3", "niks3:\n  url: s3://cache/pins/device?endpoint=s3.example&profile=fleet&scheme=https\n  aws_credentials_file: /run/secrets/cache.aws", true},
		{"S3 requires explicit credential file", "niks3:\n  url: s3://cache/pins/device?endpoint=s3.example", false},
		{"S3 requires TLS", "niks3:\n  url: s3://cache/pins/device?endpoint=s3.example&scheme=http\n  aws_credentials_file: /run/secrets/cache.aws", false},
		{"S3 embedded credentials", "niks3:\n  url: s3://cache/pins/device?endpoint=user:pass@s3.example\n  aws_credentials_file: /run/secrets/cache.aws", false},
		{"S3 arbitrary object", "niks3:\n  url: s3://cache/other?endpoint=s3.example\n  aws_credentials_file: /run/secrets/cache.aws", false},
		{"S3 traversal", "niks3:\n  url: s3://cache/pins/../secret?endpoint=s3.example\n  aws_credentials_file: /run/secrets/cache.aws", false},
		{"S3 duplicate endpoint", "niks3:\n  url: s3://cache/pins/device?endpoint=s3.example&endpoint=other\n  aws_credentials_file: /run/secrets/cache.aws", false},
		{"plaintext authentication", "niks3:\n  url: http://cache.example/pins/device\n  netrc_file: /run/secrets/cache.netrc", false},
		{"mixed sources", "remotes: [{name: origin}]\nniks3:\n  url: https://cache.example/pins/device", false},
		{"empty URL", "niks3: {}", false},
		{"local file", "niks3:\n  url: file:///pin", false},
		{"missing host", "niks3:\n  url: https:///pin", false},
		{"negative poll", "niks3:\n  url: https://cache.example/pins/device\n  poller: {period: -1}", false},
		{"bad operation", "niks3:\n  url: https://cache.example/pins/device\n  operation: reboot", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.yaml")
			assert.NoError(t, os.WriteFile(p, []byte(tc.config), 0600))
			c, err := Read(p)
			if tc.valid {
				assert.NoError(t, err)
				assert.Equal(t, "switch", c.Niks3.Operation)
				assert.Equal(t, 60, c.Niks3.Poller.Period)
				assert.Equal(t, tc.name == "manual download", c.Niks3.ManualDownload)
			} else {
				assert.Error(t, err)
			}
		})
	}
}
