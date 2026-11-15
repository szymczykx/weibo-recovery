package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadImageDistinguishesExactSamplesFromTruncation(t *testing.T) {
	const limit = 10
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("1234567890"))
		w.(http.Flusher).Flush()
		if r.URL.Path == "/long" {
			_, _ = w.Write([]byte("extra"))
		}
	}))
	defer server.Close()
	for _, tc := range []struct {
		path     string
		complete bool
		size     int64
	}{{"/exact", true, limit}, {"/long", false, -1}} {
		u, _ := url.Parse(server.URL + tc.path)
		result := readImage(t.Context(), server.Client(), u, limit)
		if result.err != nil || result.complete() != tc.complete || result.bytes != tc.size || len(result.body) != limit {
			t.Fatalf("%s: %+v", tc.path, result)
		}
	}
}

func TestRecoveryTriesLaterNodesAfterDownloadFailures(t *testing.T) {
	const address = "https://wx3.sinaimg.cn/large/test.png?sign=test"
	target, _ := url.Parse(address)
	data := pngFixture(t, 20, 12)
	find := func(_ context.Context, _ *url.URL, _ bool, _ func(string), _ func([]remoteRecord), publish func([]edge), _ func()) {
		var nodes []edge
		for i := range 8 {
			nodes = append(nodes, edge{IP: fmt.Sprintf("192.0.2.%d", i+1), Source: "测试"})
		}
		publish(nodes)
	}
	var downloads atomic.Int32
	fetch := func(_ context.Context, u *url.URL, _ edge, limit int64) response {
		if u.String() != address {
			return response{code: http.StatusNotFound}
		}
		if limit == sampleLimit {
			return response{code: http.StatusOK, body: data[:len(data)-1], bytes: int64(len(data))}
		}
		if downloads.Add(1) <= 5 {
			return response{code: http.StatusNotFound}
		}
		return response{code: http.StatusOK, body: data, bytes: int64(len(data))}
	}
	result, err := recoverPictures(t.Context(), t.TempDir(), false, []picture{{url: target}}, fetch, find, make(chan struct{}, downloadConcurrency), nil)
	if err != nil || len(result.Images) != 1 || downloads.Load() != 6 {
		t.Fatalf("later nodes were skipped: %+v, downloads=%d err=%v", result, downloads.Load(), err)
	}
}

func TestRecoveryPreservesSourcesWhenContentsMatch(t *testing.T) {
	const address = "https://wx3.sinaimg.cn/large/test.png?sign=test"
	target, _ := url.Parse(address)
	data := pngFixture(t, 20, 12)
	find := func(_ context.Context, _ *url.URL, _ bool, _ func(string), _ func([]remoteRecord), publish func([]edge), _ func()) {
		publish([]edge{{IP: "192.0.2.1", Source: "甲"}, {IP: "192.0.2.2", Source: "乙"}})
	}
	fetch := func(_ context.Context, u *url.URL, _ edge, _ int64) response {
		if u.String() != address {
			return response{code: http.StatusNotFound}
		}
		return response{code: http.StatusOK, body: data, bytes: int64(len(data))}
	}
	app := newApp()
	dir := t.TempDir()
	result, err := recoverPictures(t.Context(), dir, false, []picture{{url: target, originals: map[string]bool{address: true}}},
		fetch, find, make(chan struct{}, downloadConcurrency), func(e event) {
			if e.row != nil {
				app.appendRow(*e.row)
			}
			if e.image != nil {
				app.addRecoveredImage(*e.image)
			}
		})
	files, statErr := os.ReadDir(dir)
	if err != nil || statErr != nil || len(result.Images) != 1 || len(files) != 1 || len(app.nodeImages) != 3 {
		t.Fatalf("deduplication lost a source or copied a file: %+v files=%v nodes=%+v errors=%v %v", result, files, app.nodeImages, err, statErr)
	}
	for _, row := range app.attempts {
		img := app.savedNodeImage(row)
		if img == nil || img.Source != row.Source || !img.Original || img.IP != row.IP || img.Path != result.Images[0].Path {
			t.Fatalf("wrong saved preview for %+v: %+v", row, img)
		}
	}
}

func TestImageVerificationRejectsChangedContent(t *testing.T) {
	original := pngFixture(t, 20, 12)
	changed := slices.Clone(original)
	changed[len(changed)-1] ^= 1
	target, _ := url.Parse("https://wx1.sinaimg.cn/large/test.png")
	row := attempt{Outcome: "hit", Width: 20, Height: 12, sampleSize: len(original), sampleHash: sha256.Sum256(original), sampleComplete: true}
	_, err := verifyNodeImage(target, edge{}, row, response{code: http.StatusOK, body: changed})
	if err == nil || err.Error() != "节点当前返回的图片已变化" {
		t.Fatalf("changed contents were accepted: %v", err)
	}

	var encoded bytes.Buffer
	if err := gif.Encode(&encoded, image.NewPaletted(image.Rect(0, 0, 360, 480), []color.Color{color.Black}), nil); err != nil {
		t.Fatal(err)
	}
	smallGIF := encoded.Bytes()
	label, _, _, _ := verdict(target, response{code: http.StatusOK, body: smallGIF, bytes: int64(len(smallGIF))}, nil)
	if label != "hit" {
		t.Fatalf("a real GIF was mistaken for a placeholder: %s", label)
	}
}

func TestRecoveryStopsFallbacksAfterLargerVersion(t *testing.T) {
	target, _ := url.Parse("https://wx1.sinaimg.cn/orj360/test.png")
	small, large := pngFixture(t, 10, 10), pngFixture(t, 20, 20)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	find := func(_ context.Context, _ *url.URL, _ bool, _ func(string), _ func([]remoteRecord), _ func([]edge), _ func()) {
	}
	fetch := func(_ context.Context, u *url.URL, _ edge, _ int64) response {
		switch variant(u) {
		case "orj360":
			return response{code: http.StatusOK, body: small}
		case "mw2000":
			return response{code: http.StatusOK, body: large}
		case "orj1080", "orj960", "bmiddle":
			t.Errorf("probed %s after finding a larger fallback", u)
		}
		return response{code: http.StatusNotFound}
	}
	result, err := recoverPictures(ctx, t.TempDir(), false, []picture{{url: target}}, fetch, find, make(chan struct{}, downloadConcurrency), nil)
	if err != nil || len(result.Images) != 2 || result.Images[0].Width != 20 {
		t.Fatalf("unexpected fallback result: %+v, %v", result, err)
	}
}

func TestRecoveryCancelsQueuedProbes(t *testing.T) {
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	target, _ := url.Parse("https://wx1.sinaimg.cn/orj360/test.png")
	started := make(chan struct{}, probeConcurrency)
	finished := make(chan error, 1)
	var active atomic.Int32
	fetch := func(ctx context.Context, u *url.URL, _ edge, _ int64) response {
		if variant(u) == "images" {
			return response{code: http.StatusNotFound}
		}
		active.Add(1)
		defer active.Add(-1)
		started <- struct{}{}
		<-ctx.Done()
		return response{err: ctx.Err()}
	}
	find := func(_ context.Context, _ *url.URL, _ bool, _ func(string), _ func([]remoteRecord), publish func([]edge), _ func()) {
		var candidates []edge
		for i := range 60 {
			candidates = append(candidates, edge{IP: fmt.Sprintf("192.0.2.%d", i+1), Source: "测试"})
		}
		publish(candidates)
	}
	dir := t.TempDir()
	go func() {
		_, err := recoverPictures(ctx, dir, false, []picture{{url: target}}, fetch, find, make(chan struct{}, downloadConcurrency), nil)
		finished <- err
	}()
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	for range probeConcurrency {
		select {
		case <-started:
		case <-timeout.C:
			t.Fatal("probe workers did not start")
		}
	}
	stop()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) || active.Load() != 0 {
			t.Fatalf("queued probes did not stop cleanly: active=%d err=%v", active.Load(), err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceling did not drain the shared queue")
	}
}

func TestImageFileReplacesExistingContents(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "export.png")
	if err := os.WriteFile(destination, []byte("old image"), 0600); err != nil {
		t.Fatal(err)
	}
	want := pngFixture(t, 10, 10)
	if err := writeImageFile(destination, want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(data, want) {
		t.Fatalf("wrong export data: %v", err)
	}
	blocked := filepath.Join(dir, "directory")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeImageFile(blocked, want); err == nil {
		t.Fatal("file write replaced the directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("a failed write left temporary files: %v, %v", entries, err)
	}
}

func pngFixture(t *testing.T, width, height int) []byte {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}
