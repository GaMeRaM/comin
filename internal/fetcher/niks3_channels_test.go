package fetcher

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nlewo/comin/internal/types"
	"github.com/stretchr/testify/require"
)

func TestNiks3MainTestingSelection(t *testing.T) {
	main := "/nix/store/" + strings.Repeat("a", 32) + "-main"
	next := "/nix/store/" + strings.Repeat("b", 32) + "-next"
	test := "/nix/store/" + strings.Repeat("c", 32) + "-test"
	base := main
	testingBody, testingStatus := test, 404
	changeMain := false
	reads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pins/main-device" {
			reads++
			if changeMain && reads%2 == 0 {
				fmt.Fprint(w, next)
			} else {
				fmt.Fprint(w, main)
			}
			return
		}
		if r.URL.Path != "/pins/testing-device-"+strings.Split(strings.TrimPrefix(base, "/nix/store/"), "-")[0] {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(testingStatus)
		fmt.Fprint(w, testingBody)
	}))
	defer srv.Close()
	f := NewNiks3Fetcher(types.Niks3Fetcher{URL: srv.URL + "/pins/main-device", TestingURL: srv.URL + "/pins/testing-device", Timeout: 1}, nil)
	check := func(want string, isTesting bool) {
		t.Helper()
		path, mainPath, selected, err := f.selectPin(t.Context())
		require.NoError(t, err)
		require.Equal(t, want, path)
		require.Equal(t, main, mainPath)
		require.Equal(t, isTesting, selected)
	}
	check(main, false) // no testing branch
	testingStatus = 200
	check(test, true)
	testingBody = next
	check(next, true) // force-reset testing is allowed
	testingBody = main
	check(main, false) // equal heads use main's switch operation
	testingBody = test
	main = next
	check(main, false) // a stale test cannot mask a newer main
	base = main
	check(test, true) // CI can rebind a still-descendant test to the new main
	for _, code := range []int{403, 500} {
		testingStatus = code
		_, _, _, err := f.selectPin(t.Context())
		require.Error(t, err) // do not confuse unavailability with deletion
	}
	testingStatus = 200
	testingBody = "malformed"
	_, _, _, err := f.selectPin(t.Context())
	require.Error(t, err)
	testingStatus = 404
	check(main, false) // withdrawal returns to main
	main = base
	next = test
	changeMain = true
	reads = 0
	_, _, _, err = f.selectPin(t.Context())
	require.ErrorContains(t, err, "main changed")
}
