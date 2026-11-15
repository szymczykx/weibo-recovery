package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckHostStreamsChangedRecords(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	const httpTarget = "https://wx3.sinaimg.cn/large/test.jpg"
	firstBatch := make(chan struct{})
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/check-http":
			if r.URL.Query().Get("host") != httpTarget || strings.Join(r.URL.Query()["node"], ",") != "one,two,three" {
				t.Errorf("unexpected query: %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"request_id":"job1","nodes":{"one":["jp","日本","东京"],"two":["us","美国","纽约"],"three":["de","德国","柏林"]}}`)
		case "/check-result/job1":
			switch polls.Add(1) {
			case 1:
				fmt.Fprint(w, `{"one":[[1,0.1,"OK","200","8.8.8.8"]],"two":null,"three":null}`)
			case 2:
				select {
				case <-firstBatch:
				case <-r.Context().Done():
					return
				}
				w.WriteHeader(http.StatusBadGateway)
			case 3:
				fmt.Fprint(w, `{"one":null,"two":[[0,0.2,"Not Found",404,"1.1.1.1"]]}`)
			default:
				fmt.Fprint(w, `{"three":[[0,0.3,"Timed out",null,null]]}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := checkHostClient{server.URL, time.Millisecond}
	var updates []remoteRecord
	rows, err := client.check(ctx, "http", remoteTarget{httpTarget, "wx3.sinaimg.cn"}, []string{"one", "two", "three"}, func(batch []remoteRecord) {
		updates = append(updates, batch...)
		if len(updates) == 1 {
			close(firstBatch)
		}
	})
	if err != nil || len(rows) != 3 || len(updates) != 3 || polls.Load() != 4 {
		t.Fatalf("poll progress lost or delayed: updates=%+v rows=%+v polls=%d, %v", updates, rows, polls.Load(), err)
	}
	if rows[0].Status != "200" || rows[0].IPs[0] != "8.8.8.8" || rows[1].Status != "404" ||
		rows[2].Status != remoteRequestFailed || rows[2].Detail != "Timed out" {
		t.Fatalf("wrong final results: %+v", rows)
	}
}

func TestCheckHostCoversRequestedNodesAndKeepsQueryLocal(t *testing.T) {
	u, _ := url.Parse("https://wx3.sinaimg.cn/large/test.jpg?signature=local")
	list := probeListing{Nodes: map[string]struct{ Location []string }{"one": {}, "two": {}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/check-http":
			if address := r.URL.Query().Get("host"); address != "https://wx3.sinaimg.cn/large/test.jpg" {
				t.Errorf("unexpected remote image address %q", address)
			}
			io.WriteString(w, `{"request_id":"http1","nodes":{"one":["jp","日本","东京"]}}`)
		case "/check-dns":
			if host := r.URL.Query().Get("host"); host != u.Host {
				t.Errorf("unexpected DNS query for %q", host)
			}
			io.WriteString(w, `{"request_id":"dns1","nodes":{"one":[],"two":[]}}`)
		case "/check-result/http1":
			io.WriteString(w, `{"one":[[0,0.1,"Forbidden","403","8.8.8.8"]]}`)
		case "/check-result/dns1":
			io.WriteString(w, `{"one":[{"A":["127.0.0.1","8.8.8.8","8.8.8.8"],"AAAA":["2001:4860:4860::8888"]}],"two":[{"A":[],"AAAA":[]}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client := checkHostClient{server.URL, time.Millisecond}
	updates := make(chan []remoteRecord, 8)
	client.probeHost(ctx, u, list, func(string) {}, func(rows []remoteRecord) { updates <- rows })
	close(updates)

	app := newApp()
	for rows := range updates {
		app.updateRemoteRecords(rows)
	}
	if app.remoteCount() != "HTTP 1/2 · DNS 2/2" || app.regions[0].addresses != "8.8.8.8, 2001:4860:4860::8888" ||
		app.regions[1].http.Status != remoteNoResult || app.regions[1].dns.Status != remoteNoAddress {
		t.Fatalf("wrong remote table state: %s %+v", app.remoteCount(), app.regions)
	}
}

func TestPostPicturesIncludesAllEntries(t *testing.T) {
	var raw []any
	for i := range 10 {
		raw = append(raw, map[string]any{"original": map[string]any{"url": fmt.Sprintf("https://wx1.sinaimg.cn/large/%d.jpg", i)}})
	}
	pictures := postPictures(map[string]any{"pics": raw})
	if len(pictures) != len(raw) || !pictures[9].originals["https://wx1.sinaimg.cn/large/9.jpg"] {
		t.Fatalf("post pictures were truncated or lost their original metadata: %+v", pictures)
	}
	if id := inputID("1234"); id != "1234" {
		t.Fatalf("valid numeric ID was rejected: %q", id)
	}
	u, ok := pictureURL("https://wx1.sinaimg.cn/large/test.jpg?")
	if !ok || len(pictureTargets(picture{url: u})) != 1 || unsignedPictureURL(u).ForceQuery {
		t.Fatalf("empty query marker was copied to an inferred URL: %v", u)
	}
}
