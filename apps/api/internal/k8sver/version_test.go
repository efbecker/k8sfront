package k8sver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseAndCompare(t *testing.T) {
	v, err := Parse("v1.37.2")
	if err != nil || v.Minor != 37 || v.Plain() != "1.37.2" || v.MinorString() != "v1.37" {
		t.Fatalf("parse: %+v %v", v, err)
	}
	if _, err := Parse("1.37"); err == nil {
		t.Fatal("partial version must be rejected")
	}
	a, _ := Parse("1.36.9")
	if a.Compare(v) != -1 || v.Compare(a) != 1 || v.Compare(v) != 0 {
		t.Fatal("compare")
	}
}

func TestStableFetch(t *testing.T) {
	markers := map[string]string{"/stable.txt": "v1.37.2\n", "/stable-1.36.txt": "v1.36.6", "/stable-1.35.txt": "v1.35.9", "/stable-1.34.txt": "v1.34.13"}
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if m, ok := markers[r.URL.Path]; ok {
			w.Write([]byte(m))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	f := NewFetcher()
	f.BaseURL = srv.URL
	rel, err := f.Stable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rel) != 4 || rel[0].Version != "v1.37.2" || !rel[0].Latest || rel[3].Version != "v1.34.13" {
		t.Fatalf("unexpected: %+v", rel)
	}
	before := hits
	if _, err := f.Stable(context.Background()); err != nil || hits != before {
		t.Fatal("second call must be served from cache")
	}
}
