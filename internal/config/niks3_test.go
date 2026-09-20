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
		{"HTTPS authentication", "niks3:\n  url: https://cache.example/pins/device\n  netrc_file: /run/secrets/cache.netrc", true},
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
			} else {
				assert.Error(t, err)
			}
		})
	}
}
