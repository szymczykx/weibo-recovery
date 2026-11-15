package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image/png"
	"log"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const title = "微博图片恢复"
const defaultTablePercent float32 = 64
const defaultRemotePercent float32 = 32

type app struct {
	win                  *mygo.Window
	input                string
	remote               bool
	pane                 pane
	filter               string
	selection            string
	manualImage          bool
	status               string
	inputError           string
	saveStatus           string
	saveProblem          bool
	runState             runState
	elapsed              time.Duration
	working              bool
	saving               bool
	run                  *probeRun
	downloadSlots        chan struct{}
	previewDecode        chan struct{}
	done, total          int
	remoteLookups        int
	remoteFinished       bool
	attempts             []attempt
	regions              []remoteProbeRow
	regionIndex          map[remoteProbeKey]int
	remoteCounts         remoteProgress
	remoteRowsChanged    bool
	remoteList           ui.ListState
	images               []recovered
	imageIndex           map[string]int
	nodeImages           map[nodeKey]recovered
	activity             []string
	showNodes            bool
	nodeScroll           ui.ScrollState
	nodeHorizontal       ui.ScrollState
	nodeList             ui.ListState
	activityScroll       ui.ScrollState
	visibleAttempts      []int
	visibleCount         int
	visibleFilter        string
	visibleSort          string
	visibleDescending    bool
	hits, placeholders   int
	remoteScroll         ui.ScrollState
	remoteHorizontal     ui.ScrollState
	remoteCollapsed      bool
	remotePercent        float32
	tablePercent         float32
	viewerOpen           bool
	viewerImage          recovered
	viewerActualSize     bool
	viewerScroll         ui.ScrollState
	detail               *attempt
	detailVisible        bool
	previewRequest       *imagePreviewRequest
	nodeImage            *recovered
	nodeImageError       string
	nodeBitmap           *ui.Bitmap
	nodeImageData        []byte
	viewerBitmap         *ui.Bitmap
	revealNode           bool
	sortColumn           string
	sortDescending       bool
	remoteSortColumn     string
	remoteSortDescending bool
}

type pane string

const (
	recoveryPane pane = "恢复"
	guidePane    pane = "技术与维护"
)

type runState string

const (
	runStopping runState = "正在停止"
	runStopped  runState = "已停止"
	runFailed   runState = "探测失败"
	runEmpty    runState = "未命中"
)

func newApp() *app {
	return &app{
		remote: true, pane: recoveryPane, filter: "全部", showNodes: true, status: "等待链接",
		tablePercent: defaultTablePercent, remotePercent: defaultRemotePercent,
		downloadSlots: make(chan struct{}, downloadConcurrency),
		previewDecode: make(chan struct{}, 1),
	}
}

type probeRun struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	folder string
}

// Each window owns its run directory until the next probe or window close.
func (r *probeRun) removeFiles() {
	<-r.done
	if r.folder != "" {
		os.RemoveAll(r.folder)
	}
}

func (a *app) paste(services ui.Services) {
	if a.working {
		return
	}
	link := strings.TrimSpace(services.ReadClipboard())
	if link == "" {
		a.inputError = "剪贴板里没有链接"
		return
	}
	a.input, a.inputError = link, ""
}

func (a *app) start() {
	if a.working || a.saving {
		return
	}
	input := strings.TrimSpace(a.input)
	if _, ok := pictureURL(input); !ok && inputID(input) == "" {
		a.inputError = "请输入微博图片直链、微博详情链接或微博 ID"
		return
	}
	if a.win == nil {
		return
	}
	a.clearPreview()
	a.closeViewer()
	if a.run != nil {
		go a.run.removeFiles()
	}
	a.input, a.inputError = input, ""
	a.saveStatus, a.saveProblem = "", false
	a.working = true
	a.status = "正在准备探测"
	a.runState, a.elapsed = "", 0
	a.pane, a.filter = recoveryPane, "全部"
	a.done, a.total, a.remoteLookups = 0, 0, 0
	a.hits, a.placeholders = 0, 0
	a.attempts, a.regions, a.images = nil, nil, nil
	a.regionIndex, a.imageIndex, a.nodeImages = nil, nil, nil
	a.remoteCounts = remoteProgress{}
	a.remoteRowsChanged = true
	a.remoteFinished = false
	a.selection, a.detail = "", nil
	a.manualImage = false
	a.detailVisible, a.revealNode = false, false
	a.activity = nil
	a.showNodes = true
	a.nodeHorizontal.X, a.nodeScroll.Y = 0, 0
	a.activityScroll.X, a.activityScroll.Y = 0, 0
	a.remoteHorizontal.X, a.remoteScroll.Y = 0, 0
	a.visibleAttempts, a.visibleCount = nil, 0
	a.remoteCollapsed = false
	ctx, cancel := context.WithCancel(context.Background())
	run := &probeRun{ctx: ctx, cancel: cancel, done: make(chan struct{})}
	a.run = run
	go a.perform(run, a.win, input, a.remote, a.downloadSlots)
}

func (a *app) perform(run *probeRun, win *mygo.Window, input string, remote bool, slots chan struct{}) {
	defer close(run.done)
	defer run.cancel()
	started := time.Now()
	root, err := os.UserCacheDir()
	if err == nil {
		root = filepath.Join(root, "WeiboRecovery")
		err = os.MkdirAll(root, 0700)
		if err == nil {
			run.folder, err = os.MkdirTemp(root, "run-")
		}
	}

	var result recoveryResult
	if err == nil {
		updates := make(chan event, 128)
		flushed := make(chan struct{})
		go func() {
			defer close(flushed)
			a.displayEvents(run, win, updates)
		}()
		result, err = recovery(run.ctx, input, run.folder, remote, slots, func(e event) {
			select {
			case updates <- e:
			case <-run.ctx.Done():
			}
		})
		close(updates)
		<-flushed
	}
	win.Update(func() {
		if a.run != run {
			return
		}
		if err == nil && a.runState == runStopping {
			err = context.Canceled
		}
		for _, img := range result.Images {
			a.addRecoveredImage(img)
		}
		a.working = false
		a.elapsed = time.Since(started)
		switch {
		case errors.Is(err, context.Canceled):
			a.status, a.runState = "探测已停止", runStopped
			if a.remoteLookups > 0 {
				a.remoteFinished = true
			}
			a.remoteLookups = 0
			for i := range a.regions {
				row := a.regions[i]
				for _, record := range []remoteRecord{row.http, row.dns} {
					if record.Status == remoteWaiting {
						record.Status = remoteStopped
						a.updateRemoteRecords([]remoteRecord{record})
					}
				}
			}
			if a.detail != nil && a.nodeBitmap == nil && a.previewRequest == nil {
				if img := a.savedNodeImage(*a.detail); img != nil {
					a.loadPreview(img, nil, nil)
				}
			}
		case err != nil:
			a.status, a.runState = err.Error(), runFailed
		default:
			a.status = result.Summary
			if len(result.Images) == 0 {
				a.runState = runEmpty
			}
			a.activity = append(a.activity, result.Summary)
			a.selectBestNode()
		}
	})
}

func (a *app) displayEvents(run *probeRun, win *mygo.Window, updates <-chan event) {
	for first := range updates {
		batch := make([]event, 1, len(updates)+1)
		batch[0] = first
		for len(batch) < cap(batch) {
			batch = append(batch, <-updates)
		}
		applied := make(chan struct{})
		win.Update(func() {
			defer close(applied)
			if a.run == run {
				a.applyEvents(batch)
			}
		})
		// Backpressure result producers if the window is busy.
		select {
		case <-applied:
		case <-run.ctx.Done():
		}
	}
}

func (a *app) applyEvents(events []event) {
	var latest *event
	for i := range events {
		e := &events[i]
		if e.message != "" {
			if a.runState != runStopping {
				a.status = e.message
			}
			if len(a.activity) == 0 || a.activity[len(a.activity)-1] != e.message {
				a.activity = append(a.activity, e.message)
			}
		}
		a.updateRemoteLookups(e.remoteLookupDelta)
		if e.total > 0 {
			a.done, a.total = e.done, e.total
		}
		if e.row != nil {
			a.appendRow(*e.row)
			latest = e
		}
		if len(e.remote) > 0 {
			a.updateRemoteRecords(e.remote)
		}
		if e.image != nil {
			a.addRecoveredImage(*e.image)
		}
	}
	if latest != nil && a.runState != runStopping {
		a.followLatest(latest.sample)
	}
}

func (a *app) updateRemoteLookups(delta int) {
	if delta == 0 {
		return
	}
	a.remoteLookups += delta
	if delta > 0 && a.remoteLookups == delta {
		a.remoteCollapsed = false
		a.remoteFinished = false
	}
	if delta < 0 && a.remoteLookups == 0 {
		a.remoteCollapsed = true
		a.remoteFinished = true
	}
}

type remoteProbeKey struct{ host, node string }

type remoteProbeRow struct {
	key       remoteProbeKey
	region    string
	http, dns remoteRecord
	ips       []string
	addresses string
}

type remoteProgress struct {
	http, dns int
	stopped   int
}

func remoteTransition(previous, current string, predicate func(string) bool) int {
	switch {
	case !predicate(previous) && predicate(current):
		return 1
	case predicate(previous) && !predicate(current):
		return -1
	default:
		return 0
	}
}

func (a *app) updateRemoteRecords(records []remoteRecord) {
	if a.regionIndex == nil {
		a.regionIndex = make(map[remoteProbeKey]int)
	}
	for _, record := range records {
		node := record.Node
		if node == "" {
			node = record.Region
		}
		key := remoteProbeKey{record.Host, node}
		i, exists := a.regionIndex[key]
		if !exists {
			i = len(a.regions)
			a.regionIndex[key] = i
			a.regions = append(a.regions, remoteProbeRow{key: key, region: record.Region})
		}
		row := &a.regions[i]
		if record.Region != "" && record.Region != node {
			row.region = record.Region
		}
		previous, count := &row.http, &a.remoteCounts.http
		if record.Method == "DNS" {
			previous, count = &row.dns, &a.remoteCounts.dns
		}
		if record.Status == remoteWaiting && previous.Status != "" {
			continue
		}
		*count += remoteTransition(previous.Status, record.Status, remoteReturned)
		a.remoteCounts.stopped += remoteTransition(previous.Status, record.Status, func(status string) bool { return status == remoteStopped })
		*previous = record
		row.ips = append(row.ips[:0], row.http.IPs...)
		for _, ip := range row.dns.IPs {
			if !slices.Contains(row.ips, ip) {
				row.ips = append(row.ips, ip)
			}
		}
		row.addresses = strings.Join(row.ips, ", ")
		a.remoteRowsChanged = true
	}
}

type nodeKey struct{ address, ip, source string }

func (a *app) addRecoveredImage(img recovered) {
	if a.imageIndex == nil {
		a.imageIndex = make(map[string]int)
		a.nodeImages = make(map[nodeKey]recovered)
	}
	a.nodeImages[nodeKey{img.URL, img.IP, img.Source}] = img
	i, exists := a.imageIndex[img.SHA256]
	if !exists {
		a.imageIndex[img.SHA256] = len(a.images)
		a.images = append(a.images, img)
	} else if img.Original && !a.images[i].Original {
		a.images[i] = img
	}
	if row := a.detail; row != nil && row.URL == img.URL && row.IP == img.IP && row.Source == img.Source && a.nodeImage == nil && a.runState != runStopping {
		a.selectNodeWithSample(row, nil)
	} else if preferred := a.selected(); !a.manualImage && a.detail == nil && (preferred == nil || compareImages(img, *preferred) < 0) {
		a.chooseImage(img)
	}
}

func (a *app) appendRow(row attempt) {
	if row.Outcome == "hit" {
		a.hits++
	} else if row.Outcome == "placeholder" {
		a.placeholders++
	}
	a.attempts = append(a.attempts, row)
	if a.working && a.showNodes && a.sortColumn == "" && filterMatches(a.filter, row) && a.nodeScroll.Y >= a.nodeScroll.MaxY-1 {
		a.nodeScroll.Y = math.MaxFloat32
	}
}

func (a *app) followLatest(sample *response) {
	row := &a.attempts[len(a.attempts)-1]
	a.selectNodeWithSample(row, sample)
	a.revealNode = a.showNodes && a.sortColumn != "" && filterMatches(a.filter, *row) && a.nodeScroll.Y >= a.nodeScroll.MaxY-1
}

func (a *app) stop() {
	if a.run != nil && a.working && a.runState != runStopping {
		a.run.cancel()
		a.runState = runStopping
		a.status = "正在停止探测"
		a.cancelPreview()
	}
}

func (a *app) selected() *recovered {
	if i, ok := a.imageIndex[a.selection]; ok {
		return &a.images[i]
	}
	return nil
}

func (a *app) selectBestNode() {
	if len(a.attempts) == 0 {
		return
	}
	best := &a.attempts[len(a.attempts)-1]
	if images := orderedImages(a.images); len(images) > 0 {
		img := images[0]
		i := slices.IndexFunc(a.attempts, func(row attempt) bool {
			return row.Outcome == "hit" && row.URL == img.URL && row.IP == img.IP && row.Source == img.Source
		})
		if i >= 0 {
			best = &a.attempts[i]
		}
	}
	if !filterMatches(a.filter, *best) {
		a.filter = "全部"
		a.nodeHorizontal.X, a.nodeScroll.Y = 0, 0
	}
	a.selectNodeWithSample(best, nil)
	a.revealNode = true
}

func (a *app) savedNodeImage(row attempt) *recovered {
	if row.Outcome != "hit" {
		return nil
	}
	if img, ok := a.nodeImages[nodeKey{row.URL, row.IP, row.Source}]; ok {
		return &img
	}
	return nil
}

type imagePreviewRequest struct{ cancel context.CancelFunc }

func (a *app) cancelPreview() {
	if a.previewRequest != nil {
		a.previewRequest.cancel()
		a.previewRequest = nil
	}
}

func (a *app) clearPreview() {
	a.cancelPreview()
	a.nodeImage, a.nodeBitmap, a.nodeImageData, a.nodeImageError = nil, nil, nil, ""
}

func (a *app) selectNodeWithSample(row *attempt, sample *response) {
	same := row != nil && a.detail != nil && *row == *a.detail
	if same && (a.nodeImage != nil || a.previewRequest != nil) {
		return
	}
	a.clearPreview()
	a.detail = row
	if row != nil {
		a.manualImage = false
	}
	if !same {
		a.detailVisible, a.revealNode = false, false
	}
	if row == nil || (row.Outcome != "hit" && row.Outcome != "placeholder") {
		return
	}
	if img := a.savedNodeImage(*row); img != nil {
		a.loadPreview(img, nil, nil)
	} else if a.win != nil && a.runState != runStopping {
		a.loadPreview(nil, row, sample)
	}
}

func (a *app) chooseImage(img recovered) {
	a.clearPreview()
	a.detail, a.detailVisible, a.revealNode = nil, false, false
	a.selection = img.SHA256
	a.loadPreview(&img, nil, nil)
}

func (a *app) loadPreview(saved *recovered, row *attempt, sample *response) {
	if a.win == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	request := &imagePreviewRequest{cancel}
	a.previewRequest = request
	win, slots, decode := a.win, a.downloadSlots, a.previewDecode
	go func() {
		defer cancel()
		var img recovered
		var data []byte
		var err error
		if saved != nil {
			img = *saved
		} else {
			if sample == nil {
				timer := time.NewTimer(140 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
				}
			}
			img, data, err = fetchNodeImage(ctx, *row, sample, func(ctx context.Context, u *url.URL, e edge, limit int64) response {
				select {
				case slots <- struct{}{}:
					defer func() { <-slots }()
					return fetchCDN(ctx, u, e, limit)
				case <-ctx.Done():
					return response{err: ctx.Err()}
				}
			})
		}
		if ctx.Err() != nil {
			return
		}
		var bitmap *ui.Bitmap
		if err == nil {
			select {
			case decode <- struct{}{}:
			case <-ctx.Done():
				return
			}
			if data == nil {
				data, err = os.ReadFile(img.Path)
			}
			if err == nil && ctx.Err() == nil {
				bitmap, err = ui.DecodeBitmap(data)
			}
			<-decode
		}
		if img.Path != "" {
			data = nil
		}
		if ctx.Err() != nil {
			return
		}
		win.Update(func() {
			if a.previewRequest != request {
				return
			}
			a.previewRequest = nil
			if err != nil {
				a.nodeImageError = err.Error()
			} else {
				a.nodeImage, a.nodeBitmap, a.nodeImageData = &img, bitmap, data
			}
		})
	}()
}

func (a *app) previewSelection() (*recovered, *ui.Bitmap) {
	if row := a.detail; row != nil {
		if saved := a.savedNodeImage(*row); saved != nil {
			return saved, a.nodeBitmap
		}
		return a.nodeImage, a.nodeBitmap
	}
	return a.selected(), a.nodeBitmap
}

func (a *app) openViewer(img recovered, bitmap *ui.Bitmap) {
	a.viewerImage, a.viewerBitmap = img, bitmap
	a.viewerActualSize = false
	a.viewerScroll.X, a.viewerScroll.Y = 0, 0
	a.viewerOpen = true
}

func (a *app) closeViewer() {
	a.viewerOpen, a.viewerBitmap = false, nil
	a.viewerImage = recovered{}
}

func (a *app) save(img recovered, data []byte) {
	if a.saving || a.win == nil {
		return
	}
	a.saving = true
	a.saveStatus, a.saveProblem = "", false
	win := a.win
	go func() {
		// Read the candidate before opening the dialog, while the run owns it.
		var err error
		if data == nil {
			data, err = os.ReadFile(img.Path)
		}
		var destination string
		if err == nil {
			destination, err = mygo.Dialog.Save(mygo.SaveDialogOptions{
				Parent: win, Title: "保存图片", DefaultPath: img.Name, CreateDirectories: true,
			})
			if err == nil && destination != "" {
				err = writeImageFile(destination, data)
			}
		}
		win.Update(func() {
			a.saving = false
			if err != nil {
				a.saveStatus, a.saveProblem = "保存失败："+err.Error(), true
			} else if destination != "" {
				a.saveStatus = "已保存到 " + filepath.Base(destination)
			}
		})
	}()
}

func main() {
	live := flag.String("live", "", "仅运行真实探测并打印结果，用于验证引擎")
	out := flag.String("out", "", "指定验证输出目录")
	snapshot := flag.String("snapshot", "", "将原生界面测试帧写入 PNG")
	flag.Parse()
	if *snapshot != "" {
		a := newApp()
		tester := ui.NewTester(a.view, 1440, 900)
		f, err := os.Create(*snapshot)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, tester.Image()); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *live != "" {
		if *out == "" {
			log.Fatal("需要 --out")
		}
		if err := os.MkdirAll(*out, 0700); err != nil {
			log.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		r, err := recovery(ctx, *live, *out, true, make(chan struct{}, downloadConcurrency), func(e event) {
			if e.message != "" {
				fmt.Println(e.message)
			}
		})
		if err != nil {
			log.Fatal(err)
		}
		for _, img := range r.Images {
			fmt.Printf("%d×%d %s %s\n", img.Width, img.Height, img.SHA256, img.Path)
		}
		if len(r.Images) == 0 {
			os.Exit(1)
		}
		return
	}
	a := newApp()
	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{Title: title, Width: 1440, Height: 900, MinWidth: 1000, MinHeight: 680,
			TitleBarStyle: mygo.TitleBarHiddenInset, StateKey: "recovery", Content: ui.View(a.view)})
		a.win.OnClosed(func() {
			a.cancelPreview()
			if a.run != nil {
				a.run.cancel()
			}
		})
	})
	err := mygo.App.Run()
	if a.run != nil {
		a.run.cancel()
		a.run.removeFiles()
	}
	if err != nil {
		log.Fatal(err)
	}
}
