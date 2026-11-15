package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	_ "golang.org/x/image/webp"
)

const sampleLimit = 96 << 10
const fileLimit = 25 << 20
const probeConcurrency = 10
const downloadConcurrency = 3
const probeTimeout = 7 * time.Second
const downloadTimeout = 18 * time.Second

type response struct {
	code      int
	headers   http.Header
	body      []byte
	bytes     int64
	truncated bool
	elapsed   time.Duration
	err       error
}

type attempt struct {
	Source         string
	IP             string
	Variant        string
	URL            string
	HTTP           int
	Outcome        string
	Detail         string
	Bytes          int64
	Width          int
	Height         int
	Millis         int64
	Cache          string
	Location       string
	Host           string
	Original       bool
	sampleSize     int
	sampleHash     [sha256.Size]byte
	sampleComplete bool
}

type recovered struct {
	Name     string
	Path     string
	Variant  string
	Width    int
	Height   int
	Bytes    int
	SHA256   string
	Source   string
	IP       string
	URL      string
	Original bool
}

type recoveryResult struct {
	Summary string
	Images  []recovered
}

type event struct {
	message           string
	done, total       int
	remoteLookupDelta int
	row               *attempt
	sample            *response
	remote            []remoteRecord
	image             *recovered
}

type fetchImage func(context.Context, *url.URL, edge, int64) response
type findEdges func(context.Context, *url.URL, bool, func(string), func([]remoteRecord), func([]edge), func())

func readImage(ctx context.Context, client *http.Client, u *url.URL, limit int64) response {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return response{err: err}
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Referer", "https://weibo.com/")
	req.Header.Set("Accept", "image/webp,image/png,image/jpeg,image/gif,image/*")
	req.Header.Set("Accept-Encoding", "identity")
	reply, err := client.Do(req)
	if err != nil {
		return response{err: err, elapsed: time.Since(start)}
	}
	defer reply.Body.Close()
	result := response{code: reply.StatusCode, headers: reply.Header, bytes: reply.ContentLength}
	if reply.StatusCode == http.StatusOK {
		if limit == fileLimit && reply.ContentLength > limit {
			result.err = errors.New("图片超过 25 MB")
		} else {
			result.body, result.err = io.ReadAll(io.LimitReader(reply.Body, limit+1))
			if int64(len(result.body)) > limit {
				result.body = result.body[:limit]
				result.truncated = true
			}
		}
	} else if reply.ContentLength >= 0 && reply.ContentLength <= 4096 {
		_, _ = io.Copy(io.Discard, reply.Body)
	}
	if result.bytes < 0 && !result.truncated {
		result.bytes = int64(len(result.body))
	}
	result.elapsed = time.Since(start)
	return result
}

func (r response) complete() bool {
	return !r.truncated && r.bytes <= int64(len(r.body))
}

type cdnRoute struct{ host, ip string }

type cdnClient struct {
	mu      sync.Mutex
	clients map[cdnRoute]*http.Client
}

func (c *cdnClient) fetch(ctx context.Context, u *url.URL, e edge, limit int64) response {
	ip := publicAddress(e.IP)
	if e.IP != "" && ip == "" {
		return response{err: errors.New("非公网候选地址")}
	}
	timeout := probeTimeout
	if limit > sampleLimit {
		timeout = downloadTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return readImage(ctx, c.client(cdnRoute{u.Hostname(), ip}), u, limit)
}

func (c *cdnClient) client(route cdnRoute) *http.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if client := c.clients[route]; client != nil {
		return client
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		ForceAttemptHTTP2: true, IdleConnTimeout: 15 * time.Second, MaxIdleConnsPerHost: 1,
		TLSClientConfig: &tls.Config{ServerName: route.host, MinVersion: tls.VersionTLS12},
		DialContext:     dialer.DialContext,
	}
	if route.ip != "" {
		transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, net.JoinHostPort(route.ip, "443"))
		}
	}
	client := &http.Client{Transport: transport, CheckRedirect: publicHTTP.CheckRedirect}
	if c.clients == nil {
		c.clients = make(map[cdnRoute]*http.Client)
	}
	c.clients[route] = client
	return client
}

func (c *cdnClient) close() {
	for _, client := range c.clients {
		client.CloseIdleConnections()
	}
}

func fetchCDN(ctx context.Context, u *url.URL, e edge, limit int64) response {
	var cdn cdnClient
	defer cdn.close()
	return cdn.fetch(ctx, u, e, limit)
}

func placeholderRedirect(u *url.URL, location string) (*url.URL, bool) {
	redirect, err := u.Parse(location)
	if err != nil {
		return nil, false
	}
	target, ok := pictureURL(redirect.String())
	if !ok || !strings.HasPrefix(strings.ToLower(path.Base(target.Path)), "default_d") {
		return nil, false
	}
	return target, true
}

// CDN default GIFs observed on both sinaimg.cn and sinaimg.com.
var defaultGIFs = map[string]bool{
	"919998c0240c57b16e7cbe5c448c4d266aadf9aabf7b09611da5609905eeedf0": true,
	"77be67d34c93b7715c410bf1ad3abfbd1303c5f34e4b21ed31a5f53b5f77a047": true,
	"07f6b376a6e724f0fb1753c93a5648fb4975a204a66c651205f54715024253f3": true,
	"218a3581d44d726b6eb0f5a094982ba5ff2e43dccbf3fa3250b9f72649c55c15": true,
	"0cf64fdebb49e1082d50d28accc33ba16952a33f1df3fed8558199a54719bebb": true,
}

func verdict(u *url.URL, result response, placeholder []byte) (outcome, detail string, width, height int) {
	if result.err != nil {
		var ne net.Error
		if errors.As(result.err, &ne) && ne.Timeout() {
			return "timeout", "请求超时", 0, 0
		}
		if errors.Is(result.err, context.Canceled) {
			return "error", "已取消", 0, 0
		}
		return "error", result.err.Error(), 0, 0
	}
	switch result.code {
	case 403, 401:
		return "forbidden", "图床拒绝访问", 0, 0
	case 404:
		return "missing", "该节点没有图片", 0, 0
	case 200:
	default:
		if location := result.headers.Get("Location"); result.code >= 300 && result.code < 400 && location != "" {
			if _, ok := placeholderRedirect(u, location); ok {
				return "placeholder", "跳转到默认占位图", 0, 0
			}
		}
		return "error", fmt.Sprintf("HTTP %d", result.code), 0, 0
	}
	cfg, kind, err := image.DecodeConfig(bytes.NewReader(result.body))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 {
		return "not_image", "响应无法识别为图片", 0, 0
	}
	if len(placeholder) > 0 && bytes.Equal(placeholder, result.body) {
		return "placeholder", "与默认占位图样本一致", cfg.Width, cfg.Height
	}
	if kind == "gif" && result.complete() && len(result.body) <= 20000 {
		hash := sha256.Sum256(result.body)
		if defaultGIFs[hex.EncodeToString(hash[:])] {
			return "placeholder", "匹配已知图床占位图", cfg.Width, cfg.Height
		}
	}
	if !supportedDimensions(cfg) || result.bytes > fileLimit {
		return "error", "图片超过支持的大小", cfg.Width, cfg.Height
	}
	return "hit", fmt.Sprintf("响应为 %d × %d 图片", cfg.Width, cfg.Height), cfg.Width, cfg.Height
}

func supportedDimensions(cfg image.Config) bool {
	return cfg.Width > 0 && cfg.Height > 0 && cfg.Width <= 16000 && cfg.Height <= 16000 &&
		int64(cfg.Width)*int64(cfg.Height) <= 45_000_000
}

func cacheHeader(h http.Header) string {
	cache := h.Get("X-Cache")
	if cache == "" {
		cache = h.Get("X-Cache-Lookup")
	}
	if age := h.Get("Age"); age != "" {
		if cache != "" {
			cache += " · "
		}
		cache += "age " + age + "s"
	}
	return cache
}

func variant(u *url.URL) string {
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) > 1 {
		return parts[0]
	}
	return "源地址"
}

func resized(u *url.URL, host, size string) *url.URL {
	next := *u
	next.Host = host
	next.Path = "/" + size + "/" + path.Base(u.Path)
	return &next
}

func canResize(u *url.URL) bool {
	return u.RawQuery == "" && !u.ForceQuery && strings.Count(u.Path, "/") == 2
}

func pictureTargets(p picture) []*url.URL {
	seen := make(map[string]bool)
	var targets []*url.URL
	add := func(u *url.URL) {
		if key := u.String(); !seen[key] {
			seen[key] = true
			targets = append(targets, u)
		}
	}
	add(p.url)
	for _, u := range p.alternates {
		add(u)
	}
	// Use an unsigned address for synthetic variants. Explicit API URLs keep their query.
	for _, u := range slices.Clone(targets) {
		if !canResize(u) {
			continue
		}
		add(resized(u, u.Host, "large"))
		_, suffix, _ := strings.Cut(u.Host, ".")
		if suffix != "sinaimg.cn" && suffix != "sinaimg.com" {
			continue
		}
		for _, host := range []string{"wx1", "wx2", "wx3", "wx4"} {
			add(resized(u, host+"."+suffix, "large"))
		}
	}
	return targets
}

func isThumbnailVariant(size string) bool {
	switch size {
	case "orj360", "thumbnail", "thumb150", "wap180", "wap360":
		return true
	default:
		return false
	}
}

func hasLargerImage(images []recovered) bool {
	thumbnailPixels := 0
	for _, img := range images {
		if isThumbnailVariant(img.Variant) && !img.Original {
			thumbnailPixels = max(thumbnailPixels, img.Width*img.Height)
		}
	}
	return slices.ContainsFunc(images, func(img recovered) bool {
		return (img.Original || !isThumbnailVariant(img.Variant)) && img.Width*img.Height > thumbnailPixels
	})
}

type probeTask struct {
	u *url.URL
	e edge
}
type checkedImage struct {
	task     probeTask
	response response
}

type probeBatch struct {
	tasks   []probeTask
	results chan checkedImage
}

func (s *recoverySession) queueProbes() {
	in := make(chan probeBatch)
	tasks := make(chan probeBatch)
	s.probes = in
	s.probeWorkers.Go(func() {
		defer close(tasks)
		var pending []probeBatch
		for in != nil || len(pending) > 0 {
			var send chan probeBatch
			var task probeBatch
			if len(pending) > 0 {
				send, task = tasks, pending[0]
			}
			select {
			case <-s.ctx.Done():
				return
			case batch, ok := <-in:
				if !ok {
					in = nil
					continue
				}
				pending = append(pending, batch)
			case send <- task:
				task.tasks = task.tasks[1:]
				pending[0] = probeBatch{}
				pending = pending[1:]
				if len(task.tasks) > 0 {
					// Give another host a turn while this batch still has work.
					pending = append(pending, task)
				}
			}
		}
	})
	for range probeConcurrency {
		s.probeWorkers.Go(func() {
			for batch := range tasks {
				task := batch.tasks[0]
				result := s.probeFetch(s.ctx, task.u, task.e, sampleLimit)
				select {
				case batch.results <- checkedImage{task, result}:
				case <-s.ctx.Done():
					return
				}
			}
		})
	}
}

type imageCandidate struct {
	task   probeTask
	row    attempt
	sample *response
}

func queueHits(ctx context.Context) (chan<- imageCandidate, <-chan imageCandidate) {
	in, out := make(chan imageCandidate), make(chan imageCandidate)
	go func() {
		defer close(out)
		var pending []imageCandidate
		for in != nil || len(pending) > 0 {
			var send chan imageCandidate
			var next imageCandidate
			if len(pending) > 0 {
				send, next = out, pending[0]
			}
			select {
			case <-ctx.Done():
				return
			case item, ok := <-in:
				if !ok {
					in = nil
					continue
				}
				if len(pending) >= downloadConcurrency {
					item.sample = nil
				}
				pending = append(pending, item)
			case send <- next:
				pending[0] = imageCandidate{}
				pending = pending[1:]
			}
		}
	}()
	return in, out
}

func imageInfo(p *url.URL, data response, e edge) (recovered, error) {
	if data.err != nil {
		return recovered{}, data.err
	}
	if data.code != http.StatusOK {
		return recovered{}, fmt.Errorf("完整请求返回 HTTP %d", data.code)
	}
	if len(data.body) > fileLimit || data.truncated && len(data.body) == fileLimit {
		return recovered{}, errors.New("图片超过 25 MB")
	}
	if !data.complete() {
		return recovered{}, errors.New("节点未返回完整图片")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data.body))
	if err != nil || !supportedDimensions(cfg) {
		return recovered{}, errors.New("图片格式或尺寸异常")
	}
	if format == "jpeg" {
		format = "jpg"
	}
	hash := sha256.Sum256(data.body)
	sum := hex.EncodeToString(hash[:])
	base := path.Base(p.Path)
	base = strings.TrimSuffix(base, path.Ext(base))
	size := variant(p)
	name := base[:min(len(base), 140)] + "-" + size[:min(len(size), 64)] + "-" + sum[:8] + "." + format
	return recovered{
		Name: name, Variant: variant(p), Width: cfg.Width, Height: cfg.Height,
		Bytes: len(data.body), SHA256: sum, Source: e.Source, IP: e.IP, URL: p.String(),
	}, nil
}

func verifyNodeImage(target *url.URL, node edge, row attempt, data response) (recovered, error) {
	img, err := imageInfo(target, data, node)
	if err != nil {
		return recovered{}, err
	}
	if row.sampleSize > 0 && (len(data.body) < row.sampleSize ||
		row.sampleComplete && len(data.body) != row.sampleSize ||
		sha256.Sum256(data.body[:row.sampleSize]) != row.sampleHash) {
		return recovered{}, errors.New("节点当前返回的图片已变化")
	}
	if row.Width > 0 && (img.Width != row.Width || img.Height != row.Height) {
		return recovered{}, errors.New("节点当前响应与探测尺寸不一致")
	}
	if row.Outcome == "hit" && !row.sampleComplete {
		if label, _, _, _ := verdict(target, data, nil); label != "hit" {
			return recovered{}, errors.New("完整请求未返回图片候选")
		}
	}
	img.Original = row.Outcome == "hit" && row.Original
	return img, nil
}

func fetchNodeImage(ctx context.Context, row attempt, sample *response, fetch fetchImage) (recovered, []byte, error) {
	target, ok := pictureURL(row.URL)
	if !ok {
		return recovered{}, nil, errors.New("节点图片地址无效")
	}
	node := edge{IP: row.IP, Source: row.Source}
	var data response
	switch {
	case row.Outcome == "placeholder" && row.sampleSize == 0 && row.Location != "":
		next, ok := placeholderRedirect(target, row.Location)
		if !ok || next.Host != target.Host {
			return recovered{}, nil, errors.New("无法从所选节点读取跳转后的占位图")
		}
		target = next
		data = fetch(ctx, target, node, fileLimit)
	case sample != nil:
		data = *sample
	default:
		data = fetch(ctx, target, node, fileLimit)
	}
	img, err := verifyNodeImage(target, node, row, data)
	if err != nil {
		return recovered{}, nil, err
	}
	if row.sampleSize == 0 && row.Location == "" && row.Outcome == "placeholder" {
		if label, _, _, _ := verdict(target, data, nil); label != "placeholder" {
			return recovered{}, nil, errors.New("节点当前响应与记录的占位图不一致")
		}
	}
	return img, data.body, nil
}

type savedImage struct {
	write     func() (string, error)
	preferred recovered
}

type recoverySession struct {
	ctx           context.Context
	dir           string
	regional      bool
	fetch         fetchImage
	discoverNodes findEdges
	observer      func(event)
	probeSlots    chan struct{}
	downloadSlots chan struct{}
	probes        chan<- probeBatch
	probeWorkers  sync.WaitGroup

	eventMu     sync.Mutex
	total, done int
	saveMu      sync.Mutex
	saved       map[string]*savedImage
	lookups     map[string]*hostLookup
}

type hostLookup struct {
	nodes       []edge
	batches     chan []edge
	placeholder []byte
	sampleReady chan struct{}
	done        chan struct{}
}

func writeImageFile(destination string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(destination), ".weibo-image-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), destination)
}

func recovery(ctx context.Context, input, dir string, regional bool, downloadSlots chan struct{}, observer func(event)) (recoveryResult, error) {
	if observer != nil {
		observer(event{message: "正在读取图片地址"})
	}
	pictures, err := resolvePictures(ctx, input)
	if err != nil {
		return recoveryResult{}, err
	}
	var cdn cdnClient
	defer cdn.close()
	return recoverPictures(ctx, dir, regional, pictures, cdn.fetch, newDiscovery(ctx), downloadSlots, observer)
}

func recoverPictures(ctx context.Context, dir string, regional bool, pictures []picture, fetch fetchImage, discoverNodes findEdges, downloadSlots chan struct{}, observer func(event)) (recoveryResult, error) {
	var r recoveryResult
	session := recoverySession{
		ctx: ctx, dir: dir, regional: regional, fetch: fetch, discoverNodes: discoverNodes, observer: observer,
		probeSlots: make(chan struct{}, probeConcurrency), downloadSlots: downloadSlots,
		saved: make(map[string]*savedImage), lookups: make(map[string]*hostLookup),
	}
	session.queueProbes()
	defer func() {
		close(session.probes)
		session.probeWorkers.Wait()
	}()
	anyLarger := false
	for _, p := range pictures {
		if ctx.Err() != nil {
			break
		}
		var pictureMu sync.Mutex
		var found []recovered
		publish := func(img recovered) {
			pictureMu.Lock()
			found = append(found, img)
			pictureMu.Unlock()
		}
		targets := pictureTargets(p)
		session.emit(event{message: fmt.Sprintf("正在发现 %d 个图片地址的 CDN 候选", len(targets))})
		session.probeTargets(p, targets, publish)

		if hasLargerImage(found) {
			anyLarger = true
			continue
		}
		if ctx.Err() != nil || !canResize(p.url) {
			continue
		}
		for _, size := range []string{"mw2000", "orj1080", "orj960", "bmiddle"} {
			u := resized(p.url, p.url.Host, size)
			if slices.ContainsFunc(targets, func(other *url.URL) bool { return other.String() == u.String() }) {
				continue
			}
			session.probeTargets(p, []*url.URL{u}, publish)
			if hasLargerImage(found) {
				anyLarger = true
				break
			}
			if ctx.Err() != nil {
				break
			}
		}
	}
	for _, file := range session.saved {
		r.Images = append(r.Images, file.preferred)
	}
	r.Images = orderedImages(r.Images)
	switch {
	case ctx.Err() != nil:
		r.Summary = "探测已停止"
	case len(r.Images) == 0:
		r.Summary = "本轮未验证到可恢复图片"
	case !anyLarger:
		r.Summary = "仅找到缩略图，未找到更大版本"
	default:
		r.Summary = fmt.Sprintf("探测完成，已保存 %d 张图片候选", len(session.saved))
	}
	return r, ctx.Err()
}

func (session *recoverySession) emit(e event) {
	session.eventMu.Lock()
	defer session.eventMu.Unlock()
	session.total += e.total
	if e.row != nil {
		session.done++
	}
	e.total, e.done = session.total, session.done
	if session.observer != nil {
		session.observer(e)
	}
}

func (session *recoverySession) probeFetch(ctx context.Context, u *url.URL, e edge, limit int64) response {
	select {
	case session.probeSlots <- struct{}{}:
		defer func() { <-session.probeSlots }()
		if err := ctx.Err(); err != nil {
			return response{err: err}
		}
		return session.fetch(ctx, u, e, limit)
	case <-ctx.Done():
		return response{err: ctx.Err()}
	}
}

func (session *recoverySession) startLookup(u *url.URL) (*hostLookup, bool) {
	if result, ok := session.lookups[u.Host]; ok {
		return result, false
	}
	result := &hostLookup{
		batches: make(chan []edge, 8), sampleReady: make(chan struct{}), done: make(chan struct{}),
	}
	session.lookups[u.Host] = result
	go func() {
		defer close(result.sampleReady)
		placeholder := unsignedPictureURL(u)
		placeholder.Path = "/images/default_d_h_large.gif"
		response := session.probeFetch(session.ctx, &placeholder, edge{Source: "默认路由"}, sampleLimit)
		if response.code == http.StatusOK && response.err == nil && response.complete() {
			if _, _, err := image.DecodeConfig(bytes.NewReader(response.body)); err == nil {
				result.placeholder = response.body
			}
		}
	}()
	go func() {
		defer close(result.done)
		defer close(result.batches)
		var publishMu sync.Mutex
		seen := make(map[string]bool)
		publish := func(nodes []edge) {
			publishMu.Lock()
			defer publishMu.Unlock()
			var added []edge
			for _, node := range nodes {
				if !seen[node.IP] {
					seen[node.IP] = true
					added = append(added, node)
				}
			}
			if len(added) == 0 {
				return
			}
			result.nodes = append(result.nodes, added...)
			select {
			case result.batches <- added:
			case <-session.ctx.Done():
			}
		}
		publish([]edge{{Source: "默认路由"}})
		remoteDone := func() {}
		if session.regional {
			remoteDone = sync.OnceFunc(func() { session.emit(event{remoteLookupDelta: -1}) })
		}
		session.discoverNodes(session.ctx, u, session.regional,
			func(message string) { session.emit(event{message: u.Host + " · " + message}) },
			func(records []remoteRecord) { session.emit(event{remote: records}) }, publish, remoteDone)
		remoteDone()
	}()
	return result, true
}

func (session *recoverySession) saveHit(hit imageCandidate) (recovered, bool, error) {
	if hit.row.sampleComplete {
		sum := hex.EncodeToString(hit.row.sampleHash[:])
		session.saveMu.Lock()
		file := session.saved[sum]
		if file != nil && file.preferred.Path != "" {
			img := file.preferred
			img.URL = hit.row.URL
			img.Source, img.IP, img.Variant, img.Original = hit.row.Source, hit.row.IP, hit.row.Variant, hit.row.Original
			fresh := img.Original && !file.preferred.Original
			if fresh {
				file.preferred = img
			}
			session.saveMu.Unlock()
			return img, fresh, nil
		}
		session.saveMu.Unlock()
	}
	select {
	case session.downloadSlots <- struct{}{}:
		defer func() { <-session.downloadSlots }()
	case <-session.ctx.Done():
		return recovered{}, false, session.ctx.Err()
	}
	if err := session.ctx.Err(); err != nil {
		return recovered{}, false, err
	}
	data := hit.sample
	if data == nil {
		session.emit(event{message: "节点返回图片，正在下载完整版"})
		full := session.fetch(session.ctx, hit.task.u, hit.task.e, fileLimit)
		data = &full
	}
	if err := session.ctx.Err(); err != nil {
		return recovered{}, false, err
	}
	img, err := verifyNodeImage(hit.task.u, hit.task.e, hit.row, *data)
	if err != nil {
		return recovered{}, false, err
	}
	session.saveMu.Lock()
	file := session.saved[img.SHA256]
	if file == nil {
		file = &savedImage{write: sync.OnceValues(func() (string, error) {
			if _, _, err := image.Decode(bytes.NewReader(data.body)); err != nil {
				return "", errors.New("图片不完整或无法解码")
			}
			destination := filepath.Join(session.dir, img.SHA256+filepath.Ext(img.Name))
			return destination, writeImageFile(destination, data.body)
		})}
		session.saved[img.SHA256] = file
	}
	session.saveMu.Unlock()

	img.Path, err = file.write()
	session.saveMu.Lock()
	defer session.saveMu.Unlock()
	if err != nil {
		if session.saved[img.SHA256] == file {
			delete(session.saved, img.SHA256)
		}
		return recovered{}, false, err
	}
	fresh := file.preferred.Path == "" || img.Original && !file.preferred.Original
	if fresh {
		file.preferred = img
	}
	return img, fresh, nil
}

func (session *recoverySession) probeHost(p picture, targets []*url.URL, batches <-chan []edge, lookup *hostLookup, publish func(recovered)) {
	results := make(chan checkedImage, probeConcurrency)
	work := session.ctx.Done()
	remaining := 0
	hits := make(map[string]chan<- imageCandidate, len(targets))
	var downloaders sync.WaitGroup
	// Keep the candidate queues independent of probe result collection.
	for _, target := range targets {
		in, queue := queueHits(session.ctx)
		address := target.String()
		hits[address] = in
		downloaders.Go(func() {
			bestPixels := 0
			for hit := range queue {
				row := hit.row
				pixels := row.Width * row.Height
				// A complete sample can add a verified source without another download.
				if pixels <= bestPixels && !row.sampleComplete {
					continue
				}
				img, fresh, err := session.saveHit(hit)
				if err != nil {
					if session.ctx.Err() == nil {
						session.emit(event{message: "候选下载失败：" + err.Error()})
					}
					continue
				}
				bestPixels = max(bestPixels, pixels)
				publish(img)
				var message string
				if fresh {
					message = "已找到可预览图片"
				}
				session.emit(event{message: message, image: &img})
			}
		})
	}
collect:
	for batches != nil || remaining > 0 {
		var checked checkedImage
		select {
		case <-work:
			break collect
		case nodes, open := <-batches:
			if !open {
				batches = nil
				continue
			}
			batch := probeBatch{results: results, tasks: make([]probeTask, 0, len(nodes)*len(targets))}
			for _, node := range nodes {
				for _, target := range targets {
					batch.tasks = append(batch.tasks, probeTask{target, node})
				}
			}
			remaining += len(batch.tasks)
			session.emit(event{total: len(batch.tasks)})
			select {
			case session.probes <- batch:
			case <-work:
				break collect
			}
			continue
		case checked = <-results:
			remaining--
		}
		data := checked.response
		var placeholder []byte
		if data.code == http.StatusOK && data.err == nil {
			select {
			case <-lookup.sampleReady:
				placeholder = lookup.placeholder
			case <-work:
				break collect
			}
		}
		task := checked.task
		label, detail, width, height := verdict(task.u, data, placeholder)
		location := data.headers.Get("Location")
		if location != "" {
			detail += " · " + location
		}
		address := task.u.String()
		row := attempt{
			Source: task.e.Source, IP: task.e.IP, Variant: variant(task.u), URL: address, Host: task.u.Host,
			HTTP: data.code, Outcome: label, Detail: detail, Bytes: data.bytes, Width: width, Height: height,
			Millis: data.elapsed.Milliseconds(), Cache: cacheHeader(data.headers), Location: location,
			Original: p.originals[address],
		}
		var sample *response
		if (label == "hit" || label == "placeholder") && len(data.body) > 0 {
			row.sampleSize = len(data.body)
			row.sampleHash = sha256.Sum256(data.body)
			row.sampleComplete = data.complete()
			if row.sampleComplete {
				sample = &data
			}
		}
		session.emit(event{row: &row, sample: sample})
		if label == "hit" {
			select {
			case hits[address] <- imageCandidate{task, row, sample}:
			case <-session.ctx.Done():
			}
		}
	}
	for _, queue := range hits {
		close(queue)
	}
	downloaders.Wait()
}

func (session *recoverySession) probeTargets(p picture, targets []*url.URL, publish func(recovered)) {
	byHost := make(map[string][]*url.URL)
	var hosts []string
	for _, u := range targets {
		if len(byHost[u.Host]) == 0 {
			hosts = append(hosts, u.Host)
		}
		byHost[u.Host] = append(byHost[u.Host], u)
	}
	if session.regional {
		starting := 0
		for _, host := range hosts {
			if session.lookups[host] == nil {
				starting++
			}
		}
		if starting > 0 {
			session.emit(event{remoteLookupDelta: starting})
		}
	}
	var wg sync.WaitGroup
	for _, host := range hosts {
		addresses := byHost[host]
		discoveryURL := addresses[0]
		for _, address := range addresses {
			if p.originals[address.String()] {
				discoveryURL = address
				break
			}
		}
		lookup, fresh := session.startLookup(discoveryURL)
		batches := lookup.batches
		if !fresh {
			<-lookup.done
			batches = make(chan []edge, 1)
			batches <- lookup.nodes
			close(batches)
		}
		wg.Go(func() {
			session.probeHost(p, addresses, batches, lookup, publish)
			<-lookup.done
			<-lookup.sampleReady
		})
	}
	wg.Wait()
}

func orderedImages(rows []recovered) []recovered {
	out := slices.Clone(rows)
	slices.SortStableFunc(out, compareImages)
	return out
}

func compareImages(a, b recovered) int {
	if diff := cmp.Compare(b.Width*b.Height, a.Width*a.Height); diff != 0 {
		return diff
	}
	if a.Original != b.Original {
		if a.Original {
			return -1
		}
		return 1
	}
	return strings.Compare(a.SHA256, b.SHA256)
}
