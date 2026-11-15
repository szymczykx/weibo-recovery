package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestOfflineRecovery(t *testing.T) {
	dir := t.TempDir()
	var emitted []recovered
	var attempts []attempt
	pictures, fetch, discover, cleanup, err := simulation()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	r, err := recoverPictures(context.Background(), dir, false, pictures, fetch, discover, make(chan struct{}, downloadConcurrency), func(e event) {
		if e.row != nil {
			attempts = append(attempts, *e.row)
		}
		if e.image != nil && e.message != "" {
			emitted = append(emitted, *e.image)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 36 || len(r.Images) != 1 || len(emitted) != 1 {
		t.Fatalf("attempts=%d images=%d preview events=%d", len(attempts), len(r.Images), len(emitted))
	}
	for _, verdict := range []string{"hit", "placeholder", "forbidden", "not_image", "timeout", "missing"} {
		if !slices.ContainsFunc(attempts, func(row attempt) bool { return row.Outcome == verdict }) {
			t.Errorf("missing %q status", verdict)
		}
	}
	result := r.Images[0]
	if !slices.ContainsFunc(attempts, func(row attempt) bool {
		return row.Outcome == "hit" && row.Width == result.Width && row.Height == result.Height
	}) {
		t.Fatal("node record is missing the decoded dimensions")
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != result.SHA256 || result.Width != 480 || result.Height != 320 {
		t.Fatal("wrong checksum or image dimensions")
	}
	if _, err := os.Stat(filepath.Join(dir, "report.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected report file: %v", err)
	}
}

func TestProbeOverlapsDiscoveryAndImageVersions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	target, _ := url.Parse("https://wx1.sinaimg.cn/orj360/test.png")
	placeholderStarted := make(chan struct{})
	largeStarted := make(chan struct{})
	fetch := func(ctx context.Context, u *url.URL, _ edge, _ int64) response {
		if u.Host != target.Host {
			return response{code: http.StatusNotFound}
		}
		switch u.Path {
		case "/images/default_d_h_large.gif":
			close(placeholderStarted)
		case "/orj360/test.png":
			select {
			case <-largeStarted:
			case <-ctx.Done():
				return response{err: ctx.Err()}
			}
		case "/large/test.png":
			close(largeStarted)
		}
		return response{code: http.StatusNotFound}
	}
	find := func(ctx context.Context, u *url.URL, _ bool, _ func(string), _ func([]remoteRecord), _ func([]edge), _ func()) {
		if u.Host != target.Host {
			return
		}
		select {
		case <-placeholderStarted:
		case <-ctx.Done():
		}
	}
	var firstBatch []string
	_, err := recoverPictures(ctx, t.TempDir(), false, []picture{{url: target}}, fetch, find, make(chan struct{}, downloadConcurrency), func(e event) {
		if e.row != nil && strings.HasPrefix(e.row.URL, "https://"+target.Host+"/") && len(firstBatch) < 2 {
			firstBatch = append(firstBatch, e.row.Variant)
		}
	})
	if err != nil || !slices.Contains(firstBatch, "large") || !slices.Contains(firstBatch, "orj360") {
		t.Fatalf("primary versions did not overlap: %v, %v", firstBatch, err)
	}
}

func TestLimitDestinations(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "::1", "169.254.169.254", "10.1.2.3", "198.18.0.47", "100.64.0.1", "fc00::1", "::ffff:127.0.0.1"} {
		if publicAddress(address) != "" {
			t.Errorf("allowed non-public address %s", address)
		}
	}
	for _, link := range []string{"https://wx3.sinaimg.cn.example.com/a.jpg", "https://user@wx3.sinaimg.cn/a.jpg", "https://wx3.sinaimg.cn:8000/a.jpg", "https://wx3.sinaimg.cn/%2e%2e/a.jpg"} {
		if _, ok := pictureURL(link); ok {
			t.Errorf("allowed invalid URL %s", link)
		}
	}
	if got := mid(inputID("https://weibo.com/0/000a0010")); got != "100000062" {
		t.Errorf("unexpected decoded post ID: %s", got)
	}
}

func TestThumbnailPublishedWhileOtherHostsStillProbing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	thumb, _ := url.Parse("https://wx1.sinaimg.cn/orj360/test.png")
	original, _ := url.Parse("https://tvax1.sinaimg.cn/large/test.png?signature=test")
	p := picture{url: thumb, alternates: []*url.URL{original}, originals: map[string]bool{original.String(): true}}
	encode := func(width, height int) []byte {
		var data bytes.Buffer
		if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
			t.Fatal(err)
		}
		return data.Bytes()
	}
	small, large := encode(20, 15), encode(80, 60)
	thumbnailPublished := make(chan struct{})
	allHostsStarted := make(chan struct{})
	started := make(chan string, 5)
	go func() {
		for range 5 {
			select {
			case <-started:
			case <-ctx.Done():
				return
			}
		}
		close(allHostsStarted)
	}()
	find := func(ctx context.Context, u *url.URL, regional bool, notify func(string), _ func([]remoteRecord), publish func([]edge), _ func()) {
		if regional {
			started <- u.Host
			notify(u.Host)
		}
		publish([]edge{{IP: "192.0.2.1", Source: "节点一"}, {IP: "192.0.2.2", Source: "节点二"}})
	}
	fetch := func(ctx context.Context, u *url.URL, _ edge, _ int64) response {
		switch u.String() {
		case thumb.String():
			select {
			case <-allHostsStarted:
				return response{code: http.StatusOK, body: small}
			case <-ctx.Done():
				return response{err: ctx.Err()}
			}
		case original.String():
			select {
			case <-thumbnailPublished:
				return response{code: http.StatusOK, body: large}
			case <-ctx.Done():
				return response{err: ctx.Err()}
			}
		}
		return response{code: http.StatusNotFound}
	}
	probed := make(map[string]int)
	var images []recovered
	result, err := recoverPictures(ctx, t.TempDir(), true,
		[]picture{p}, fetch, find, make(chan struct{}, downloadConcurrency), func(e event) {
			if e.row != nil {
				probed[e.row.URL]++
			}
			if e.image != nil && e.message != "" {
				images = append(images, *e.image)
				if len(images) == 1 && e.image.URL == thumb.String() {
					close(thumbnailPublished)
				}
			}
		})
	if err != nil || len(images) != 2 || images[0].URL != thumb.String() ||
		len(result.Images) != 2 || result.Images[0].URL != original.String() || !result.Images[0].Original {
		t.Fatalf("unexpected recovery: %v, images=%v, events=%v", err, result.Images, images)
	}
	a := newApp()
	a.working = true
	const placeholderIP = "192.0.2.10"
	for i, img := range images {
		appendTestAttempt(a, attempt{URL: img.URL, IP: img.IP, Source: img.Source, Outcome: "hit"}, nil)
		a.addRecoveredImage(img)
		shown, _ := a.previewSelection()
		if shown == nil || shown.URL != img.URL || a.detail.IP != img.IP {
			t.Fatalf("step %d: preview did not show the current hit", i)
		}
		appendTestAttempt(a, attempt{URL: img.URL, IP: placeholderIP, Outcome: "placeholder"}, nil)
		a.addRecoveredImage(img)
		if shown, _ := a.previewSelection(); shown != nil || a.detail.IP != placeholderIP {
			t.Fatalf("step %d: latest placeholder did not replace the previous image", i)
		}
	}
	a.working = false
	a.selectBestNode()
	if shown, _ := a.previewSelection(); shown == nil || shown.URL != original.String() || a.detail.IP == placeholderIP {
		t.Fatal("completed probe did not select the best validated image")
	}
	for _, target := range pictureTargets(p) {
		if probed[target.String()] != 3 {
			t.Errorf("%s probed %d nodes, want 3", target, probed[target.String()])
		}
	}
}

func TestLargerImageUsesDecodedDimensions(t *testing.T) {
	images := []recovered{
		{Variant: "orj360", Width: 360, Height: 454},
		{Variant: "large", Width: 360, Height: 454},
	}
	if hasLargerImage(images) {
		t.Fatal("large URL returned the same pixel dimensions as the thumbnail")
	}
	images[1].Width, images[1].Height = 1117, 1408
	if !hasLargerImage(images) {
		t.Fatal("did not recognize larger decoded dimensions")
	}
}

func TestListedNodesIncludesEveryNode(t *testing.T) {
	list := probeListing{Nodes: make(map[string]struct{ Location []string })}
	for _, region := range strings.Fields("at au br ca ch de es fi fr gb hk id in it jp kr nl nz pl pt ro se sg tr tw ua us vn za") {
		list.Nodes[region+"1.node"] = struct{ Location []string }{[]string{region}}
	}
	expectedRegions := len(list.Nodes)
	list.Nodes["jp2.node"] = struct{ Location []string }{[]string{"JP"}}
	list.Nodes["unknown.node"] = struct{ Location []string }{}
	got, regions := listedNodes(list)
	if len(got) != len(list.Nodes) || regions != expectedRegions || !slices.Contains(got, "za1.node") ||
		!slices.Contains(got, "jp2.node") || !slices.IsSorted(got) {
		t.Fatalf("unexpected node selection: %d regions, %v", regions, got)
	}
}

func TestNodeVerificationConsumesCandidatesBeforeDiscoveryCompletes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	target, _ := url.Parse("https://wx1.sinaimg.cn/large/test.png?signature=test")
	defaultVerified := make(chan struct{})
	firstVerified := make(chan struct{})
	lastStarted := make(chan struct{})
	lastVerified := make(chan struct{})
	remoteDone := make(chan struct{})
	const firstIP = "192.0.2.1"
	const lastIP = "192.0.2.2"
	find := func(ctx context.Context, _ *url.URL, _ bool, _ func(string), _ func([]remoteRecord), publish func([]edge), finishRemote func()) {
		select {
		case <-defaultVerified:
		case <-ctx.Done():
			return
		}
		first := edge{IP: firstIP, Source: "DoH"}
		publish([]edge{first})
		select {
		case <-firstVerified:
		case <-ctx.Done():
			return
		}
		publish([]edge{first, {IP: lastIP, Source: "Check-Host"}})
		select {
		case <-lastStarted:
		case <-ctx.Done():
			return
		}
		finishRemote()
		select {
		case <-lastVerified:
		case <-ctx.Done():
		}
	}
	fetch := func(ctx context.Context, u *url.URL, node edge, _ int64) response {
		if u.Path == target.Path && node.IP == lastIP {
			close(lastStarted)
			select {
			case <-remoteDone:
			case <-ctx.Done():
				return response{err: ctx.Err()}
			}
		}
		return response{code: http.StatusNotFound}
	}
	var ips []string
	var done, total int
	_, err := recoverPictures(ctx, t.TempDir(), true,
		[]picture{{url: target}}, fetch, find, make(chan struct{}, downloadConcurrency), func(e event) {
			done, total = e.done, e.total
			if e.remoteLookupDelta < 0 {
				close(remoteDone)
			}
			if e.row == nil {
				return
			}
			ips = append(ips, e.row.IP)
			switch {
			case e.row.IP == "" && len(ips) == 1:
				close(defaultVerified)
			case e.row.IP == firstIP && len(ips) == 2:
				close(firstVerified)
			case e.row.IP == lastIP && len(ips) == 3:
				close(lastVerified)
			}
		})
	if err != nil || !slices.Equal(ips, []string{"", firstIP, lastIP}) || done != 3 || total != 3 {
		t.Fatalf("streamed probes: %v, %d/%d, err=%v", ips, done, total, err)
	}
}

func TestTitleBarVersionClearsWindowControls(t *testing.T) {
	const version = "1.2.1"
	const width = 1440
	previousVersion := mygo.App.Version()
	mygo.App.SetVersion(version)
	t.Cleanup(func() { mygo.App.SetVersion(previousVersion) })

	view := ui.NewTester(newApp().view, width, 900)
	before, ok := view.Find("v" + version)
	if !ok {
		t.Fatal("version is missing")
	}
	bar := ui.TitleBar{Height: 32, Right: 138, Bottom: 32}
	view.SetTitleBar(bar)
	after, ok := view.Find("v" + version)
	if !ok || after.X+after.W > width-bar.Right {
		t.Fatalf("version overlaps the Windows caption buttons: %+v", after)
	}

	view.SetTitleBar(ui.TitleBar{Height: 28, Left: 72, Bottom: 26})
	after, ok = view.Find("v" + version)
	if !ok || after != before {
		t.Fatalf("left-side controls moved the version: got %+v, want %+v", after, before)
	}
}

func TestNativeNavigationAndPaste(t *testing.T) {
	a := newApp()
	view := ui.NewTester(a.view, 1120, 780)
	if err := view.Click("技术与维护"); err != nil || !view.HasText("探测流程") {
		t.Fatalf("guide not visible: %v, %v", err, view.Texts())
	}
	if err := view.Click("恢复"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("暂无节点记录") {
		t.Fatal(view.Texts())
	}
	if err := view.Click("微博链接或图片地址"); err != nil {
		t.Fatal(err)
	}
	view.SetClipboard("https://example.org/not-weibo.jpg")
	view.Key(ui.Cmd, ui.KeyV)
	view.Key(0, ui.KeyEnter)
	if a.working || a.inputError != "" || a.status != "等待链接" || a.input != "https://example.org/not-weibo.jpg" {
		t.Fatalf("paste or Enter changed probe state: input=%q status=%q", a.input, a.status)
	}
	if err := view.Click("开始探测"); err != nil || a.inputError == "" {
		t.Fatalf("button did not validate the input: %v, %q", err, a.status)
	}
	if err := view.Click("微博链接或图片地址"); err != nil {
		t.Fatal(err)
	}
	view.Key(ui.Cmd, ui.KeyA)
	view.Type("https://wx1.sinaimg.cn/large/test.jpg")
	if a.inputError != "" || a.working {
		t.Fatalf("editing did not clear the input error: %q", a.inputError)
	}
}

func TestRecoveryWorkspaceLiveTable(t *testing.T) {
	a := newApp()
	a.working = true
	a.updateRemoteRecords([]remoteRecord{{Region: "东京", Method: "HTTP", Status: "403", IPs: []string{"8.8.8.8"}}})
	view := ui.NewTester(a.view, 1120, 780)
	if !view.HasText("图片预览") || !view.HasText("等待本机节点返回") {
		t.Fatal(view.Texts())
	}
	if view.HasText("粘贴并开始") || view.HasText("离线模拟") {
		t.Fatal("obsolete input controls are still visible")
	}
	for i := range 24 {
		appendTestAttempt(a, attempt{Source: "日本探针", IP: fmt.Sprintf("192.0.2.%d", i+1), Variant: "large", Outcome: "placeholder", HTTP: 301, Cache: "HIT", Detail: "跳转到默认占位图"}, nil)
	}
	view.Frame()
	if !view.HasText("24 条记录") || !view.HasText("跳转到默认占位图 · 日本探针 · 192.0.2.24") || !view.HasText("缓存") {
		t.Fatal(view.Texts())
	}
	if a.nodeScroll.Y == 0 || a.nodeScroll.Y != a.nodeScroll.MaxY {
		first, _ := view.Find("192.0.2.1")
		last, _ := view.Find("192.0.2.24")
		save, _ := view.Find("保存图片…")
		t.Fatalf("live table did not follow new rows: %v / %v (first %v, last %v, save %v)", a.nodeScroll.Y, a.nodeScroll.MaxY, first, last, save)
	}
	viewport := a.nodeScroll.Y
	tableHeader, ok := view.Find("节点 IP")
	if !ok {
		t.Fatal(view.Texts())
	}
	view.Scroll(tableHeader.X+5, tableHeader.Y+75, 0, -130)
	if a.nodeScroll.Y >= viewport {
		t.Fatalf("table did not scroll back: before %v, after %v", viewport, a.nodeScroll.Y)
	}
	viewport = a.nodeScroll.Y
	appendTestAttempt(a, attempt{IP: "203.0.113.80", Outcome: "forbidden", HTTP: 403}, nil)
	view.Frame()
	if a.detail == nil || a.detail.IP != "203.0.113.80" || view.HasText("下载来源：") {
		t.Fatal("latest probe should replace the previous preview")
	}
	if a.nodeScroll.Y != viewport {
		t.Fatalf("appending moved the current viewport: before %v, after %v", viewport, a.nodeScroll.Y)
	}
	if err := view.Click("筛选：拒绝"); err != nil || a.filter != "拒绝访问" || !view.HasText("203.0.113.80") {
		t.Fatalf("result tab did not filter the table: %v, %q", err, a.filter)
	}
	if !view.HasText("远程探测") || !view.HasText("东京") {
		t.Fatal(view.Texts())
	}
	a.attempts[0].Outcome = "hit"
	a.attempts[0].URL = "https://wx1.sinaimg.cn/large/test.png"
	a.addRecoveredImage(recovered{URL: a.attempts[0].URL, IP: a.attempts[0].IP, Source: a.attempts[0].Source,
		SHA256: "first", Width: 1117, Height: 1408, Variant: "large"})
	a.selectNodeWithSample(&a.attempts[0], nil)
	view.Frame()
	if err := view.Click("收起节点区"); err != nil {
		t.Fatal(err)
	}
	if a.showNodes || view.HasText("候选 IP") || !view.HasText("1117 × 1408") {
		t.Fatal(view.Texts())
	}
	appendTestAttempt(a, attempt{IP: "203.0.113.81", Outcome: "hit", HTTP: 200}, nil)
	view.Frame()
	if !view.HasText("26 条本机记录") {
		t.Fatal(view.Texts())
	}
	if err := view.Click("展开节点表"); err != nil {
		t.Fatal(err)
	}
	if !a.showNodes || !view.HasText("东京") || a.pane != "恢复" {
		t.Fatal(view.Texts())
	}
}

func TestRemoteListCombinesLiveResultsByHostAndNode(t *testing.T) {
	a := newApp()
	a.working = true
	const host = "wx1.sinaimg.cn"
	httpRow := remoteRecord{Host: host, Node: "jp1.node.check-host.net", Region: "日本 · 东京", Method: "HTTP", Status: remoteWaiting}
	dnsRow := remoteRecord{Host: host, Node: httpRow.Node, Region: httpRow.Region, Method: "DNS", Status: remoteWaiting}
	a.updateRemoteRecords([]remoteRecord{httpRow, dnsRow})
	view := ui.NewTester(a.view, 1440, 900)
	if !view.HasText("等待返回") || !view.HasText("HTTP 0/1 · DNS 0/1") || !view.HasText("等待本机节点返回") {
		t.Fatal(view.Texts())
	}
	dnsRow.Status, dnsRow.IPs = "已返回", []string{"8.8.8.8"}
	a.updateRemoteRecords([]remoteRecord{dnsRow})
	view.Frame()
	if !view.HasText("HTTP 0/1 · DNS 1/1") || !view.HasText("8.8.8.8") || !view.HasText("等待返回") {
		t.Fatal(view.Texts())
	}
	pendingHTTP := httpRow
	httpRow.Status = "200"
	sameCity := dnsRow
	sameCity.Node = "jp2.node.check-host.net"
	otherHost := dnsRow
	otherHost.Host = "wx2.sinaimg.cn"
	a.updateRemoteRecords([]remoteRecord{httpRow, pendingHTTP, sameCity, otherHost})
	view.Frame()
	if rows := a.regions; len(rows) != 3 || rows[0].http.Status != "200" || !view.HasText("HTTP 1/3 · DNS 3/3") {
		t.Fatalf("unexpected remote rows: %+v", rows)
	}
	a.remoteLookups, a.done, a.total = 1, 2, 7
	view.Frame()
	if !view.HasText("2 / 7 已验证") || !view.HasText("验证中") || !view.HasText("HTTP 1/3 · DNS 3/3") {
		t.Fatal(view.Texts())
	}
	if err := view.Click("折叠远程探测"); err != nil || !a.remoteCollapsed || view.HasText("候选 IP") ||
		!view.HasText("2 / 7 已验证") {
		t.Fatalf("collapse remote list: %v, %v", err, view.Texts())
	}
	if err := view.Click("展开远程探测"); err != nil || a.remoteCollapsed ||
		!view.HasText("候选 IP") || !view.HasText("HTTP 1/3 · DNS 3/3") {
		t.Fatalf("expand remote list: %v, %v", err, view.Texts())
	}
	if err := view.Click("按HTTP排序"); err != nil || !view.HasText("HTTP ↑") {
		t.Fatalf("remote HTTP sort: %v, %v", err, view.Texts())
	}
	if err := view.Click("按HTTP排序"); err != nil || !view.HasText("HTTP ↓") || a.sortColumn != "" {
		t.Fatalf("remote reverse sort: %v, %v", err, view.Texts())
	}
	a.updateRemoteLookups(-1)
	view.Frame()
	if !a.remoteCollapsed || !view.HasText("2 / 7 已验证") {
		t.Fatal("remote completion should collapse only the remote list")
	}
	if err := view.Click("展开远程探测"); err != nil {
		t.Fatal(err)
	}
	a.updateRemoteLookups(0)
	view.Frame()
	if a.remoteCollapsed || !view.HasText("HTTP ↓") {
		t.Fatal("local updates should keep the remote list open and sorted")
	}

	empty := newApp()
	empty.working = true
	empty.updateRemoteLookups(1)
	empty.updateRemoteLookups(-1)
	if state, _ := empty.remotePhase(); state != "无远程结果" || !empty.remoteCollapsed {
		t.Fatalf("failed discovery appears to be pending: %s", state)
	}
}

func TestSplitAndViewer(t *testing.T) {
	a := newApp()
	img := recovered{SHA256: "first", Width: 1117, Height: 1408, Variant: "large"}
	a.addRecoveredImage(img)
	a.nodeBitmap = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, img.Width, img.Height)))
	appendTestAttempt(a, attempt{IP: "192.0.2.1", Source: "日本探针", Outcome: "hit", Cache: "HIT"}, nil)
	view := ui.NewTester(a.view, 1220, 780)
	before, ok := view.Find("图片预览")
	if !ok {
		t.Fatal(view.Texts())
	}
	// The split has 6 DIP padding and a 14 DIP inset inside the preview.
	x, y := before.X-20, before.Y+125
	view.Press(x, y)
	view.Move(x-130, y)
	view.Release(x-130, y)
	after, _ := view.Find("图片预览")
	if a.tablePercent >= defaultTablePercent || after.X >= before.X-90 {
		t.Fatalf("drag did not widen preview: share %v, x %.1f → %.1f", a.tablePercent, before.X, after.X)
	}
	ipHeader, _ := view.Find("节点 IP")
	ipBody, _ := view.Find("192.0.2.1")
	if ipHeader.X != ipBody.X {
		t.Fatalf("header x %.1f differs from row x %.1f", ipHeader.X, ipBody.X)
	}
	if a.nodeHorizontal.MaxX <= 0 {
		// The table has a fixed minimum content width to preserve all columns.
		t.Fatalf("narrowed table lost horizontal overflow: %+v", a.nodeHorizontal)
	}
	if err := view.Click("查看大图"); err != nil || !a.viewerOpen {
		t.Fatalf("viewer did not open: %v", err)
	}
	if !view.HasText("大图 · 1117 × 1408 · large") {
		t.Fatal(view.Texts())
	}
	if err := view.Click("原始像素"); err != nil || !a.viewerActualSize || a.viewerScroll.MaxY <= 0 {
		t.Fatalf("original pixel view not scrollable: %v, offset %+v", err, a.viewerScroll)
	}
	view.Key(0, ui.KeyEscape)
	if a.viewerOpen || a.viewerBitmap != nil {
		t.Fatal("Escape did not close the viewer and release its bitmap")
	}
}

func TestPreviewFollowsSelectedNode(t *testing.T) {
	a := newApp()
	const large = "https://wx3.sinaimg.cn/large/abc.jpg"
	const thumb = "https://wx3.sinaimg.cn/orj360/abc.jpg"
	a.addRecoveredImage(recovered{URL: large, IP: "192.0.2.1", Source: "洛杉矶", Variant: "large", SHA256: "large", Width: 1117, Height: 1408})
	a.addRecoveredImage(recovered{URL: thumb, IP: "192.0.2.2", Source: "华沙", Variant: "orj360", SHA256: "thumb", Width: 360, Height: 454})
	a.attempts = []attempt{
		{URL: large, IP: "192.0.2.1", Source: "洛杉矶", Outcome: "hit", Width: 1117, Height: 1408, Millis: 400},
		{URL: thumb, IP: "192.0.2.2", Source: "华沙", Outcome: "hit", Width: 360, Height: 454, Millis: 100},
		{URL: thumb, IP: "192.0.2.3", Source: "圣保罗", Outcome: "hit", Width: 360, Height: 454, Millis: 200},
		{URL: thumb, IP: "192.0.2.4", Source: "东京", Outcome: "placeholder", Detail: "跳转到默认占位图", Millis: 300},
	}
	view := ui.NewTester(a.view, 1440, 900)
	if err := view.Click("192.0.2.2"); err != nil || !view.HasText("下载来源：华沙") {
		t.Fatalf("should display the selected node's saved image: %v", err)
	}
	for _, ip := range []string{"192.0.2.3", "192.0.2.4"} {
		if err := view.Click(ip); err != nil {
			t.Fatal(err)
		}
		if img, _ := a.previewSelection(); img != nil || view.HasText("下载来源：") || view.HasText("保存图片…") {
			t.Fatalf("preview for %s leaked another node's image", ip)
		}
	}
	if !view.HasText("跳转到默认占位图 · 东京 · 192.0.2.4") {
		t.Fatal(view.Texts())
	}
	a.nodeImage = &recovered{Source: "东京", IP: "192.0.2.4", Width: 360, Height: 480}
	a.nodeBitmap = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 360, 480)))
	view.Frame()
	if !view.HasText("下载来源：东京") || !view.HasText("360 × 480") || view.HasText("保存图片…") {
		t.Fatal(view.Texts())
	}
	if err := view.Click("按延迟排序"); err != nil || !slices.Equal(a.sortedAttempts(), []int{1, 2, 3, 0}) || !view.HasText("延迟 ↑") {
		t.Fatalf("ascending sort failed: %v, %v", err, a.sortedAttempts())
	}
	if err := view.Click("按延迟排序"); err != nil || !slices.Equal(a.sortedAttempts(), []int{0, 3, 2, 1}) || !view.HasText("延迟 ↓") {
		t.Fatalf("descending sort failed: %v, %v", err, a.sortedAttempts())
	}
	if a.detail == nil || a.detail.IP != "192.0.2.4" || !view.HasText("下载来源：东京") {
		t.Fatal("sorting changed the selected node")
	}
	a.filter = "占位图"
	a.selectBestNode()
	view.Frame()
	if a.filter != "全部" || a.detail == nil || a.detail.IP != "192.0.2.1" || !view.HasText("下载来源：洛杉矶") {
		t.Fatal("finished probe did not reveal the largest complete image's node")
	}

	if err := view.Click("选择已恢复图片"); err != nil {
		t.Fatal(err)
	}
	if err := view.ChooseMenuItem("缩略图 · orj360 · 360 × 454 · thumb"); err != nil {
		t.Fatal(err)
	}
	if a.detail != nil || !view.HasText("下载来源：华沙") {
		t.Fatalf("saved image menu did not switch the preview: %v", view.Texts())
	}
}

func TestNodeTableOnlyBuildsVisibleRows(t *testing.T) {
	a := newApp()
	a.working = true
	for i := range 2400 {
		a.appendRow(attempt{Source: fmt.Sprint("节点 ", i), Outcome: "missing"})
	}
	view := ui.NewTester(a.view, 1440, 900)
	if !view.HasText("2400 条记录") || !view.HasText("节点 2399") || !view.HasText("节点来源") || view.HasText("节点 1") {
		t.Fatalf("table did not render a window at the tail (number of texts: %d)", len(view.Texts()))
	}
	if len(view.Texts()) > 350 {
		t.Fatalf("built too many offscreen rows: %d labels", len(view.Texts()))
	}
	a.nodeScroll.Y = 0
	view.Frame()
	if !view.HasText("节点 0") || view.HasText("节点 2399") {
		t.Fatal("scrolling did not render rows at the start")
	}
}

func TestFetchNodePlaceholder(t *testing.T) {
	const base = "https://wx3.sinaimg.cn"
	var payload bytes.Buffer
	if err := png.Encode(&payload, image.NewRGBA(image.Rect(0, 0, 16, 12))); err != nil {
		t.Fatal(err)
	}
	const location = "./images/default_d_h_large.gif#101"
	row := attempt{URL: base + "/large/abc.jpg", IP: "8.8.8.8", Source: "节点 A", Outcome: "placeholder", Location: location}
	calls := 0
	fetch := func(_ context.Context, u *url.URL, node edge, _ int64) response {
		calls++
		if u.String() != base+"/large/images/default_d_h_large.gif" || node.IP != row.IP || node.Source != row.Source {
			t.Fatalf("unexpected placeholder request: %s, %+v", u, node)
		}
		return response{code: 200, body: payload.Bytes()}
	}
	img, data, err := fetchNodeImage(context.Background(), row, nil, fetch)
	if err != nil || img.Width != 16 || img.Height != 12 || !bytes.Equal(data, payload.Bytes()) || calls != 1 {
		t.Fatalf("placeholder not loaded: %+v, %v", img, err)
	}
	row.Location = "https://other.sinaimg.cn/default_d_h_large.gif"
	if _, _, err := fetchNodeImage(context.Background(), row, nil, fetch); err == nil || calls != 1 {
		t.Fatal("cross-host placeholder should not be requested")
	}
}

func TestImageVersionUsesPostMetadata(t *testing.T) {
	const original = "https://wx3.sinaimg.cn/large/abc.jpg"
	const thumb = "https://wx3.sinaimg.cn/orj360/abc.jpg"
	pictures := postPictures(map[string]any{"pic_infos": map[string]any{"abc": map[string]any{
		"original":  map[string]any{"url": original},
		"thumbnail": map[string]any{"url": thumb},
	}}})
	if len(pictures) != 1 || pictures[0].url.String() != original || !pictures[0].originals[original] || pictures[0].originals[thumb] {
		t.Fatalf("incorrect post provenance: %+v", pictures)
	}
	for _, test := range []struct {
		variant  string
		original bool
		want     string
	}{
		{"orj360", false, "缩略图"},
		{"large", false, "大图"},
		{"largest", false, "大图"},
		{"large", true, "微博标记原图"},
	} {
		if got := imageVersion(recovered{Variant: test.variant, Original: test.original}); got != test.want {
			t.Errorf("imageVersion(%q, %v) = %q; want %q", test.variant, test.original, got, test.want)
		}
	}
}

func appendTestAttempt(a *app, row attempt, sample *response) {
	a.appendRow(row)
	if a.working {
		a.followLatest(sample)
	}
}

func simulation() ([]picture, fetchImage, findEdges, func(), error) {
	img := image.NewRGBA(image.Rect(0, 0, 480, 320))
	for y := 0; y < 320; y++ {
		for x := 0; x < 480; x++ {
			palette := []color.RGBA{{238, 241, 245, 255}, {81, 102, 207, 255}, {203, 214, 255, 255}, {32, 42, 55, 255}}
			i := 0
			switch {
			case (x-185)*(x-185)+(y-148)*(y-148) < 68*68:
				i = 1
			case x > 244 && x < 355 && y > 94 && y < 203:
				i = 2
			case x > 244 && x < 355 && y > 218 && y < 226:
				i = 3
			}
			img.SetRGBA(x, y, palette[i])
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		return nil, nil, nil, func() {}, err
	}
	payload := encoded.Bytes()
	// The test server's certificate is trusted only by this simulation client.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/images/default_d_h_large.gif" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.URL.Query().Get("edge") {
		case "1":
			w.Header().Set("Location", "/images/default_d_h_large.gif")
			w.WriteHeader(301)
		case "2":
			w.WriteHeader(403)
		case "3":
			w.WriteHeader(404)
		case "4":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<html>测试响应</html>")
		case "5":
			select {
			case <-r.Context().Done():
			case <-time.After(900 * time.Millisecond):
				w.WriteHeader(503)
			}
		default:
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(payload)
		}
	}))
	client := server.Client()
	client.Timeout = 650 * time.Millisecond
	client.CheckRedirect = publicHTTP.CheckRedirect
	fetch := func(ctx context.Context, u *url.URL, e edge, limit int64) response {
		target, _ := url.Parse(server.URL)
		target.Path = u.Path
		target.RawQuery = url.Values{"edge": {e.IP}}.Encode()
		return readImage(ctx, client, target, limit)
	}
	local := func(_ context.Context, _ *url.URL, _ bool, _ func(string), _ func([]remoteRecord), publish func([]edge), _ func()) {
		publish([]edge{{"1", "本地模拟 1"}, {"2", "本地模拟 2"}, {"3", "本地模拟 3"}, {"4", "本地模拟 4"}, {"5", "本地模拟 5"}, {"6", "本地模拟 6"}, {"7", "本地模拟 7"}, {"8", "本地模拟 8"}})
	}
	u, _ := url.Parse("https://wx1.sinaimg.cn/large/offline-sample.png")
	return []picture{{url: u}}, fetch, local, server.Close, nil
}
