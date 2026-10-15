package service

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestLibraryAPI(t *testing.T) {
	// This endpoint describes compiled code, so it must not need a full snapshot.
	h := Handler(exceptionOnlyReadback{storage.NewMemory()}, "library-token")
	for _, test := range []struct {
		path, token string
		status      int
	}{{"/v1/library", "", 401}, {"/v1/library", "library-token", 200}, {"/v1/library?unknown=1", "library-token", 400}} {
		r := httptest.NewRequest("GET", test.path, nil)
		r.Header.Set("Authorization", "Bearer "+test.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatalf("%s: %d %s", test.path, w.Code, w.Body.String())
		}
		if w.Code == 200 {
			var actual experiment.Catalog
			if err := json.Unmarshal(w.Body.Bytes(), &actual); err != nil {
				t.Fatal(err)
			}
			expected, err := experiment.BuiltinCatalog()
			if err != nil || !reflect.DeepEqual(actual, expected) {
				t.Fatal("service altered compiled pins")
			}
		}
	}
}
