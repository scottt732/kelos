package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/kelos-dev/kelos/internal/source"
)

func TestProxy_RecordsMetrics(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"number":1}]`))
	}))
	defer upstream.Close()

	p := newProxy(upstream.URL, time.Minute, nil)
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	before := testutil.ToFloat64(githubAPIRequestsTotal.WithLabelValues("GET", "200", "issues", "miss"))
	upstreamBefore := testutil.ToFloat64(githubAPIUpstreamRequestsTotal.WithLabelValues("GET", "200", "issues", "miss"))

	req, _ := http.NewRequest("GET", proxyServer.URL+"/repos/owner/repo/issues", nil)
	req.Header.Set(source.UpstreamBaseURLHeader, upstream.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	after := testutil.ToFloat64(githubAPIRequestsTotal.WithLabelValues("GET", "200", "issues", "miss"))
	upstreamAfter := testutil.ToFloat64(githubAPIUpstreamRequestsTotal.WithLabelValues("GET", "200", "issues", "miss"))
	if after != before+1 {
		t.Errorf("expected miss counter to increment by 1, got delta %f", after-before)
	}
	if upstreamAfter != upstreamBefore+1 {
		t.Errorf("expected upstream miss counter to increment by 1, got delta %f", upstreamAfter-upstreamBefore)
	}
}

func TestProxy_RecordsFreshCacheHitMetric(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			t.Error("Did not expect upstream revalidation for a fresh cache hit")
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"number":1}]`))
	}))
	defer upstream.Close()

	p := newProxy(upstream.URL, time.Minute, nil)
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	doGET := func() {
		req, _ := http.NewRequest("GET", proxyServer.URL+"/repos/owner/repo/pulls", nil)
		req.Header.Set(source.UpstreamBaseURLHeader, upstream.URL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	freshHitBefore := testutil.ToFloat64(githubAPIRequestsTotal.WithLabelValues("GET", "200", "pulls", "fresh_hit"))
	missBefore := testutil.ToFloat64(githubAPIRequestsTotal.WithLabelValues("GET", "200", "pulls", "miss"))
	upstreamMissBefore := testutil.ToFloat64(githubAPIUpstreamRequestsTotal.WithLabelValues("GET", "200", "pulls", "miss"))

	doGET()
	doGET()

	freshHitAfter := testutil.ToFloat64(githubAPIRequestsTotal.WithLabelValues("GET", "200", "pulls", "fresh_hit"))
	missAfter := testutil.ToFloat64(githubAPIRequestsTotal.WithLabelValues("GET", "200", "pulls", "miss"))
	upstreamMissAfter := testutil.ToFloat64(githubAPIUpstreamRequestsTotal.WithLabelValues("GET", "200", "pulls", "miss"))

	if missAfter != missBefore+1 {
		t.Errorf("expected 1 miss, got delta %f", missAfter-missBefore)
	}
	if freshHitAfter != freshHitBefore+1 {
		t.Errorf("expected 1 fresh_hit, got delta %f", freshHitAfter-freshHitBefore)
	}
	if upstreamMissAfter != upstreamMissBefore+1 {
		t.Errorf("expected 1 upstream miss, got delta %f", upstreamMissAfter-upstreamMissBefore)
	}
}

func TestProxy_RecordsRevalidatedCacheHitMetric(t *testing.T) {
	var reqCount int
	now := time.Unix(1700000000, 0)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount++
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"number":1}]`))
	}))
	defer upstream.Close()

	p := newProxy(upstream.URL, time.Second, nil)
	p.now = func() time.Time { return now }
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	doGET := func() {
		req, _ := http.NewRequest("GET", proxyServer.URL+"/repos/owner/repo/pulls", nil)
		req.Header.Set(source.UpstreamBaseURLHeader, upstream.URL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	hitBefore := testutil.ToFloat64(githubAPIRequestsTotal.WithLabelValues("GET", "200", "pulls", "revalidated_hit"))
	missBefore := testutil.ToFloat64(githubAPIRequestsTotal.WithLabelValues("GET", "200", "pulls", "miss"))
	upstreamMissBefore := testutil.ToFloat64(githubAPIUpstreamRequestsTotal.WithLabelValues("GET", "200", "pulls", "miss"))
	upstreamRevalidateBefore := testutil.ToFloat64(githubAPIUpstreamRequestsTotal.WithLabelValues("GET", "304", "pulls", "revalidate"))

	// First request: cache miss.
	doGET()
	// Second request: stale entry is revalidated and served from cache.
	now = now.Add(2 * time.Second)
	doGET()

	hitAfter := testutil.ToFloat64(githubAPIRequestsTotal.WithLabelValues("GET", "200", "pulls", "revalidated_hit"))
	missAfter := testutil.ToFloat64(githubAPIRequestsTotal.WithLabelValues("GET", "200", "pulls", "miss"))
	upstreamMissAfter := testutil.ToFloat64(githubAPIUpstreamRequestsTotal.WithLabelValues("GET", "200", "pulls", "miss"))
	upstreamRevalidateAfter := testutil.ToFloat64(githubAPIUpstreamRequestsTotal.WithLabelValues("GET", "304", "pulls", "revalidate"))

	if missAfter != missBefore+1 {
		t.Errorf("expected 1 miss, got delta %f", missAfter-missBefore)
	}
	if hitAfter != hitBefore+1 {
		t.Errorf("expected 1 hit, got delta %f", hitAfter-hitBefore)
	}
	if upstreamMissAfter != upstreamMissBefore+1 {
		t.Errorf("expected 1 upstream miss, got delta %f", upstreamMissAfter-upstreamMissBefore)
	}
	if upstreamRevalidateAfter != upstreamRevalidateBefore+1 {
		t.Errorf("expected 1 upstream revalidate, got delta %f", upstreamRevalidateAfter-upstreamRevalidateBefore)
	}
	if reqCount != 2 {
		t.Errorf("expected 2 upstream requests, got %d", reqCount)
	}
}

func TestProxy_RecordsNonGETSkipMetric(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":1}`))
	}))
	defer upstream.Close()

	p := newProxy(upstream.URL, time.Minute, nil)
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	before := testutil.ToFloat64(githubAPIRequestsTotal.WithLabelValues("POST", "201", "issues", "skip"))
	upstreamBefore := testutil.ToFloat64(githubAPIUpstreamRequestsTotal.WithLabelValues("POST", "201", "issues", "skip"))

	req, _ := http.NewRequest("POST", proxyServer.URL+"/repos/owner/repo/issues", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	after := testutil.ToFloat64(githubAPIRequestsTotal.WithLabelValues("POST", "201", "issues", "skip"))
	upstreamAfter := testutil.ToFloat64(githubAPIUpstreamRequestsTotal.WithLabelValues("POST", "201", "issues", "skip"))
	if after != before+1 {
		t.Errorf("expected non-GET skip counter to increment by 1, got delta %f", after-before)
	}
	if upstreamAfter != upstreamBefore+1 {
		t.Errorf("expected upstream non-GET skip counter to increment by 1, got delta %f", upstreamAfter-upstreamBefore)
	}
}

func TestProxy_RecordsSingleflightAsOneUpstreamRequest(t *testing.T) {
	gate := make(chan struct{})
	upstreamStarted := make(chan struct{})
	var upstreamStartedOnce sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamStartedOnce.Do(func() {
			close(upstreamStarted)
		})
		<-gate
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"number":1}]`))
	}))
	defer upstream.Close()

	p := newProxy(upstream.URL, time.Minute, nil)
	const concurrency = 5
	releaseProxy := make(chan struct{})
	var releaseProxyOnce sync.Once
	var arrived atomic.Int32
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if arrived.Add(1) == concurrency {
			releaseProxyOnce.Do(func() {
				close(releaseProxy)
			})
		}
		<-releaseProxy
		p.ServeHTTP(w, r)
	}))
	defer proxyServer.Close()

	proxiedMissBefore := testutil.ToFloat64(githubAPIRequestsTotal.WithLabelValues("GET", "200", "issues", "miss"))
	proxiedFreshHitBefore := testutil.ToFloat64(githubAPIRequestsTotal.WithLabelValues("GET", "200", "issues", "fresh_hit"))
	upstreamBefore := testutil.ToFloat64(githubAPIUpstreamRequestsTotal.WithLabelValues("GET", "200", "issues", "miss"))

	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("GET", proxyServer.URL+"/repos/owner/repo/issues", nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("request failed: %v", err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}()
	}

	select {
	case <-upstreamStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream request")
	}
	close(gate)
	wg.Wait()

	proxiedMissAfter := testutil.ToFloat64(githubAPIRequestsTotal.WithLabelValues("GET", "200", "issues", "miss"))
	proxiedFreshHitAfter := testutil.ToFloat64(githubAPIRequestsTotal.WithLabelValues("GET", "200", "issues", "fresh_hit"))
	upstreamAfter := testutil.ToFloat64(githubAPIUpstreamRequestsTotal.WithLabelValues("GET", "200", "issues", "miss"))

	proxiedDelta := (proxiedMissAfter - proxiedMissBefore) + (proxiedFreshHitAfter - proxiedFreshHitBefore)
	if proxiedDelta != concurrency {
		t.Errorf("expected %d proxied requests across miss and fresh_hit, got delta %f", concurrency, proxiedDelta)
	}
	if upstreamAfter != upstreamBefore+1 {
		t.Errorf("expected 1 upstream miss request, got delta %f", upstreamAfter-upstreamBefore)
	}
}
