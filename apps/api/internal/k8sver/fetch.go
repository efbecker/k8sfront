package k8sver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Release is a supported minor line with its latest stable patch.
type Release struct {
	Version string `json:"version"` // e.g. v1.37.2
	Minor   string `json:"minor"`   // e.g. v1.37
	Latest  bool   `json:"latest"`
}

// Fetcher reads stable markers from dl.k8s.io (stable.txt and stable-1.N.txt),
// the same source kubeadm uses, and caches the result.
type Fetcher struct {
	BaseURL string // default https://dl.k8s.io/release
	Client  *http.Client
	Minors  int           // how many minor lines to return (default 4: the supported window)
	TTL     time.Duration // cache duration (default 1h)

	mu      sync.Mutex
	cached  []Release
	fetched time.Time
}

// NewFetcher returns a Fetcher with defaults.
func NewFetcher() *Fetcher {
	return &Fetcher{BaseURL: "https://dl.k8s.io/release", Client: &http.Client{Timeout: 15 * time.Second}, Minors: 4, TTL: time.Hour}
}

func (f *Fetcher) marker(ctx context.Context, name string) (Version, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.BaseURL+"/"+name, nil)
	if err != nil {
		return Version{}, err
	}
	resp, err := f.Client.Do(req)
	if err != nil {
		return Version{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Version{}, fmt.Errorf("%s: HTTP %d", name, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return Version{}, err
	}
	return Parse(strings.TrimSpace(string(body)))
}

// Stable returns the latest patch of the newest supported minor lines, newest first.
func (f *Fetcher) Stable(ctx context.Context) ([]Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cached != nil && time.Since(f.fetched) < f.TTL {
		return f.cached, nil
	}
	latest, err := f.marker(ctx, "stable.txt")
	if err != nil {
		return nil, fmt.Errorf("fetch stable Kubernetes version: %w", err)
	}
	n := f.Minors
	if n <= 0 {
		n = 4
	}
	type res struct {
		v   Version
		err error
	}
	results := make([]res, n)
	var wg sync.WaitGroup
	for i := 0; i < n && latest.Minor-i >= 0; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i == 0 {
				results[i] = res{v: latest}
				return
			}
			v, err := f.marker(ctx, fmt.Sprintf("stable-%d.%d.txt", latest.Major, latest.Minor-i))
			results[i] = res{v, err}
		}(i)
	}
	wg.Wait()
	var out []Release
	for i, r := range results {
		if r.err != nil || r.v.Major == 0 {
			continue
		}
		out = append(out, Release{Version: r.v.String(), Minor: r.v.MinorString(), Latest: i == 0})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, _ := Parse(out[i].Version)
		b, _ := Parse(out[j].Version)
		return a.Compare(b) > 0
	})
	f.cached, f.fetched = out, time.Now()
	return out, nil
}
