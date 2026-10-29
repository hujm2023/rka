package teaching

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJSONCallClassifiesMissingKnowledgeBase(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		want             int
	}{
		{"missing base", "/api/knowledge-bases/rka_index", `{"detail":"Knowledge base 'rka_index' not found"}`, http.StatusNotFound},
		{"encoded name", "/api/knowledge-bases/a%20b", `{"detail":"Knowledge base 'a b' not found"}`, http.StatusNotFound},
		{"server fault", "/api/knowledge-bases/rka_index", `{"detail":"storage unavailable"}`, http.StatusInternalServerError},
		{"different resource", "/api/knowledge-bases/rka_index", `{"detail":"Knowledge base 'other' not found"}`, http.StatusInternalServerError},
		{"progress failure", "/api/knowledge-bases/rka_index/progress", `{"detail":"Knowledge base 'rka_index' not found"}`, http.StatusInternalServerError},
		{"invalid response", "/api/knowledge-bases/rka_index", `unavailable`, http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500); _, _ = io.WriteString(w, tc.body) }))
			defer upstream.Close()
			d := &deepTutorWSBackend{baseURL: upstream.URL, client: upstream.Client()}
			err := d.jsonCall(t.Context(), http.MethodGet, tc.path, nil, "", nil)
			var failure *UpstreamError
			if !errors.As(err, &failure) || failure.Status != tc.want {
				t.Fatalf("error = %v, want status %d", err, tc.want)
			}
		})
	}
}
