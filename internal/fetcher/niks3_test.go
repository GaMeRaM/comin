package fetcher

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nlewo/comin/internal/broker"
	"github.com/nlewo/comin/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNiks3NetrcAuthenticationAndRotation(t *testing.T) {
	password := "first-password"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "reader" || pass != password {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, testOutput)
	}))
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "netrc")
	f := NewNiks3Fetcher(types.Niks3Fetcher{URL: srv.URL, NetrcFile: p, Timeout: 1}, nil)
	f.client.Transport = srv.Client().Transport
	_, err := f.fetch(t.Context())
	require.Error(t, err)
	for _, pass := range []string{"first-password", "rotated-password"} {
		password = pass
		require.NoError(t, os.WriteFile(p, []byte("machine 127.0.0.1 login reader password "+pass), 0600))
		out, err := f.fetch(t.Context())
		require.NoError(t, err)
		assert.Equal(t, testOutput, out)
	}
	require.NoError(t, os.WriteFile(p, []byte("machine unrelated.example login reader password private-value"), 0600))
	_, err = f.fetch(t.Context())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "private-value")
}

func TestNiks3RedirectCannotLeaveOrigin(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("must not follow the cross-origin redirect")
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer srv.Close()
	f := NewNiks3Fetcher(types.Niks3Fetcher{URL: srv.URL, Timeout: 1}, nil)
	_, err := f.fetch(t.Context())
	require.ErrorContains(t, err, "original origin")
}

const testOutput = "/nix/store/0123456789abcdfghijklmnpqrsvwxyz-nixos-system-device"

func TestNiks3PinResponse(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		valid      bool
	}{
		{"plain pin", testOutput, 200, true},
		{"newline", testOutput + "\n", 200, true},
		{"missing", testOutput, 404, false},
		{"server error", testOutput, 503, false},
		{"empty", "", 200, false},
		{"JSON management response", `{"store_path":"` + testOutput + `"}`, 200, false},
		{"multiple paths", testOutput + "\n" + testOutput, 200, false},
		{"derivation", testOutput + ".drv", 200, false},
		{"subpath", testOutput + "/bin/switch-to-configuration", 200, false},
		{"argument", "--help", 200, false},
		{"oversized", strings.Repeat("x", 4097), 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			f := NewNiks3Fetcher(types.Niks3Fetcher{URL: srv.URL, Timeout: 1}, nil)
			out, err := f.fetch(context.Background())
			if tc.valid {
				assert.NoError(t, err)
				assert.Equal(t, testOutput, out)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestNiks3PinCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	f := NewNiks3Fetcher(types.Niks3Fetcher{URL: srv.URL, Timeout: 10}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := f.fetch(ctx)
	assert.Error(t, err)
}

func TestNiks3ReconcilesUnchangedPinAndReturnsSnapshots(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, testOutput) }))
	defer srv.Close()
	b := broker.New()
	b.Start()
	defer b.Stop()
	events := b.Subscribe()
	f := NewNiks3Fetcher(types.Niks3Fetcher{URL: srv.URL, Timeout: 1}, b)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Start(ctx)
	for range 2 {
		f.TriggerFetch(nil)
		select {
		case e := <-events:
			assert.True(t, e.GetFetched().Updated)
			assert.Equal(t, testOutput, e.GetFetched().GetNiks3Status().StorePath)
		case <-time.After(time.Second):
			t.Fatal("fetch event missing")
		}
	}
	snapshot := f.GetState()
	snapshot.GetNiks3Status().StorePath = "mutated"
	assert.Equal(t, testOutput, f.GetState().GetNiks3Status().StorePath)
}
