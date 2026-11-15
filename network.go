package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/131.0.0.0 Safari/537.36"

var imageHost = regexp.MustCompile(`^[a-z0-9-]+\.sinaimg\.(cn|com)$`)
var postPath = regexp.MustCompile(`^/(?:[0-9]+|detail|status)/([0-9A-Za-z]{1,24})/?$`)
var postID = regexp.MustCompile(`^[a-zA-Z0-9]{8,24}$|^[0-9]{1,24}$`)
var imagePath = regexp.MustCompile(`^/[a-zA-Z0-9_./-]+\.(?i:jpg|jpeg|png|gif|webp)$`)

func pictureURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User != nil || !imageHost.MatchString(strings.ToLower(u.Hostname())) ||
		(u.Scheme != "https" && u.Scheme != "http") || (u.Port() != "" && u.Port() != "443" && u.Port() != "80") ||
		!imagePath.MatchString(u.EscapedPath()) || path.Clean(u.Path) != u.Path {
		return nil, false
	}
	u.Scheme, u.Host, u.Fragment = "https", strings.ToLower(u.Hostname()), ""
	return u, true
}

func unsignedPictureURL(u *url.URL) url.URL {
	copy := *u
	copy.RawQuery, copy.ForceQuery = "", false
	return copy
}

func inputID(raw string) string {
	s := strings.TrimSpace(raw)
	if postID.MatchString(s) {
		return s
	}
	u, err := url.Parse(s)
	if err != nil || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	if !slices.Contains([]string{"weibo.com", "www.weibo.com", "m.weibo.cn"}, strings.ToLower(u.Host)) {
		return ""
	}
	match := postPath.FindStringSubmatch(u.Path)
	if match == nil {
		return ""
	}
	return match[1]
}

func mid(id string) string {
	if strings.Trim(id, "0123456789") == "" {
		return id
	}
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	out := ""
	for len(id) > 0 {
		offset := max(0, len(id)-4)
		n := 0
		for _, c := range id[offset:] {
			n = n*62 + strings.IndexRune(alphabet, c)
		}
		if offset > 0 {
			out = fmt.Sprintf("%07d", n) + out
		} else {
			out = fmt.Sprint(n) + out
		}
		id = id[:offset]
	}
	return out
}

var publicHTTP = &http.Client{
	Timeout:       8 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func readJSON(ctx context.Context, endpoint, accept, referer string, value any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", browserUA)
	if referer != "" {
		req.Header.Set("Referer", referer)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	resp, err := publicHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(value)
}

type picture struct {
	url        *url.URL
	alternates []*url.URL
	originals  map[string]bool
}

func postPictures(raw map[string]any) []picture {
	byName := make(map[string]int)
	var pictures []picture
	var add func(any, bool)
	add = func(value any, original bool) {
		if object, ok := value.(map[string]any); ok {
			add(object["url"], original)
			return
		}
		s, ok := value.(string)
		if !ok {
			return
		}
		u, ok := pictureURL(s)
		if !ok {
			return
		}
		key := path.Base(u.Path)
		i, exists := byName[key]
		if exists {
			p := &pictures[i]
			if *p.url != *u && !slices.ContainsFunc(p.alternates, func(v *url.URL) bool { return *v == *u }) {
				p.alternates = append(p.alternates, u)
			}
		} else {
			i = len(pictures)
			byName[key] = i
			pictures = append(pictures, picture{url: u})
		}
		if original {
			if pictures[i].originals == nil {
				pictures[i].originals = make(map[string]bool)
			}
			pictures[i].originals[u.String()] = true
		}
	}
	var walk func(map[string]any)
	walk = func(post map[string]any) {
		if infos, ok := post["pic_infos"].(map[string]any); ok {
			keys := make([]string, 0, len(infos))
			if ids, ok := post["pic_ids"].([]any); ok {
				for _, item := range ids {
					if id, ok := item.(string); ok {
						keys = append(keys, id)
					}
				}
			}
			var remaining []string
			for id := range infos {
				if !slices.Contains(keys, id) {
					remaining = append(remaining, id)
				}
			}
			slices.Sort(remaining)
			for _, id := range append(keys, remaining...) {
				if info, ok := infos[id].(map[string]any); ok {
					for _, field := range []string{"original", "largest", "large", "mw2000", "bmiddle", "thumbnail"} {
						add(info[field], field == "original")
					}
				}
			}
		}
		if pics, ok := post["pics"].([]any); ok {
			for _, item := range pics {
				if pic, ok := item.(map[string]any); ok {
					for _, field := range []string{"original", "original_pic", "largest", "large", "url"} {
						add(pic[field], field == "original" || field == "original_pic")
					}
				}
			}
		}
		add(post["original_pic"], true)
		for _, field := range []string{"data", "retweeted_status", "status"} {
			if sub, ok := post[field].(map[string]any); ok {
				walk(sub)
			}
		}
	}
	walk(raw)
	return pictures
}

func resolvePictures(ctx context.Context, input string) ([]picture, error) {
	if u, ok := pictureURL(input); ok {
		return []picture{{url: u}}, nil
	}
	id := inputID(input)
	if id == "" {
		return nil, errors.New("请粘贴微博图片直链、微博详情链接或微博 ID")
	}
	for _, service := range []struct{ endpoint, referer string }{
		{"https://weibo.com/ajax/statuses/show?id=" + id, "https://weibo.com/"},
		{"https://m.weibo.cn/statuses/show?id=" + mid(id), "https://m.weibo.cn/"},
	} {
		var post map[string]any
		if readJSON(ctx, service.endpoint, "application/json", service.referer, &post) == nil {
			if files := postPictures(post); len(files) != 0 {
				return files, nil
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("微博接口未返回图片地址，可能需要登录。请在浏览器打开该微博并复制图片直链。")
}

type edge struct {
	IP     string
	Source string
}

type remoteTarget struct{ address, host string }

type remoteRecord struct {
	Node   string
	Host   string
	Region string
	Method string
	Status string
	IPs    []string
	Detail string
}

type regionHint struct{ Name, Subnet string }

var subnetHints = []regionHint{
	{"无地区提示", ""},
	{"日本", "1.0.16.0/24"}, {"香港", "203.80.96.0/24"}, {"新加坡", "165.21.0.0/24"},
	{"台湾", "168.95.1.0/24"}, {"韩国", "168.126.63.0/24"}, {"美国", "8.8.8.0/24"},
	{"德国", "80.156.0.0/24"}, {"英国", "81.2.69.0/24"}, {"澳大利亚", "1.128.0.0/24"},
	{"加拿大", "24.114.0.0/24"}, {"法国", "194.51.0.0/24"}, {"巴西", "200.147.0.0/24"},
}

var globalIPv6 = netip.MustParsePrefix("2000::/3")
var reserved = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("3fff::/20"),
}

func publicAddress(s string) string {
	ip, err := netip.ParseAddr(s)
	if err != nil || ip.Zone() != "" {
		return ""
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.Is6() && !globalIPv6.Contains(ip) {
		return ""
	}
	for _, p := range reserved {
		if p.Contains(ip) {
			return ""
		}
	}
	return ip.String()
}

func ecsLookup(ctx context.Context, host string, hints []regionHint, publish func([]edge)) {
	type answer struct {
		Status int
		Answer []struct {
			Type int
			Data string
		}
	}
	var wg sync.WaitGroup
	for _, hint := range hints {
		wg.Go(func() {
			query := url.Values{"name": {host}, "type": {"A"}}
			if hint.Subnet != "" {
				query.Set("edns_client_subnet", hint.Subnet)
			}
			services := []struct{ Name, URL string }{{"Google DoH", "https://dns.google/resolve"}, {"AliDNS", "https://dns.alidns.com/resolve"}}
			if hint.Subnet == "" {
				services = append(services, struct{ Name, URL string }{"Cloudflare DoH", "https://cloudflare-dns.com/dns-query"})
			}
			for _, service := range services {
				var result answer
				if ctx.Err() != nil {
					return
				}
				accept := "application/json"
				if service.Name == "Cloudflare DoH" {
					accept = "application/dns-json"
				}
				if readJSON(ctx, service.URL+"?"+query.Encode(), accept, "", &result) != nil || result.Status != 0 {
					continue
				}
				var edges []edge
				for _, a := range result.Answer {
					if a.Type == 1 {
						if ip := publicAddress(a.Data); ip != "" {
							edges = append(edges, edge{ip, service.Name + " · " + hint.Name})
						}
					}
				}
				if len(edges) > 0 {
					publish(edges)
					break
				}
			}
		})
	}
	wg.Wait()
}

const checkHost = "https://check-host.net"
const remoteWaiting = "等待返回"
const remoteNoResult = "未返回"
const remoteSubmitFailed = "提交失败"
const remoteStopped = "已停止"

func remoteReturned(status string) bool {
	switch status {
	case "", remoteWaiting, remoteNoResult, remoteSubmitFailed, remoteStopped:
		return false
	default:
		return true
	}
}

type probeListing struct {
	Nodes map[string]struct{ Location []string }
}
type probeJob struct {
	ID    string `json:"request_id"`
	Nodes map[string][]string
}

func listedNodes(list probeListing) ([]string, int) {
	nodes := make([]string, 0, len(list.Nodes))
	regions := make(map[string]bool)
	for name, node := range list.Nodes {
		if name == "" {
			continue
		}
		nodes = append(nodes, name)
		if len(node.Location) > 0 {
			if region := strings.ToLower(strings.TrimSpace(node.Location[0])); region != "" {
				regions[region] = true
			}
		}
	}
	slices.Sort(nodes)
	return nodes, len(regions)
}

const remoteRequestFailed = "请求失败"
const remoteNoAddress = "无地址"

type checkHostClient struct {
	baseURL      string
	pollInterval time.Duration
}

func (c checkHostClient) read(ctx context.Context, path string, value any) error {
	return readJSON(ctx, c.baseURL+path, "application/json", "", value)
}

var requestID = regexp.MustCompile(`^[a-zA-Z0-9]{1,64}$`)

func (c checkHostClient) check(ctx context.Context, kind string, target remoteTarget, nodes []string, update func([]remoteRecord)) ([]remoteRecord, error) {
	params := url.Values{"host": {target.address}, "node": nodes}
	var job probeJob
	if err := c.read(ctx, "/check-"+kind+"?"+params.Encode(), &job); err != nil {
		return nil, err
	}
	accepted := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if _, ok := job.Nodes[node]; ok {
			accepted = append(accepted, node)
		}
	}
	if !requestID.MatchString(job.ID) || len(accepted) == 0 {
		return nil, errors.New("没有可用探针")
	}

	pollCtx, stop := context.WithTimeout(ctx, 23*time.Second)
	defer stop()
	records := make(map[string]json.RawMessage, len(accepted))
	var pollErr error
poll:
	for {
		var snapshot map[string]json.RawMessage
		pollErr = c.read(pollCtx, "/check-result/"+job.ID, &snapshot)
		if pollErr == nil {
			var changed []remoteRecord
			for _, node := range accepted {
				raw := snapshot[node]
				if len(raw) == 0 || string(raw) == "null" || bytes.Equal(records[node], raw) {
					continue
				}
				records[node] = raw
				changed = append(changed, decodeRemoteRecord(kind, target.host, node, job.Nodes[node], raw))
			}
			if len(changed) > 0 {
				update(changed)
			}
			if len(records) == len(accepted) {
				break
			}
		}
		timer := time.NewTimer(c.pollInterval)
		select {
		case <-pollCtx.Done():
			timer.Stop()
			break poll
		case <-timer.C:
		}
	}

	status := remoteNoResult
	if ctx.Err() != nil {
		status = remoteStopped
		pollErr = ctx.Err()
	}
	result := make([]remoteRecord, 0, len(accepted))
	for _, node := range accepted {
		row := decodeRemoteRecord(kind, target.host, node, job.Nodes[node], records[node])
		if row.Status == remoteWaiting {
			row.Status = status
		}
		result = append(result, row)
	}
	return result, pollErr
}

func remoteRegion(node string, location []string) string {
	if len(location) >= 3 {
		return location[1] + " · " + location[2]
	}
	return node
}

func decodeRemoteRecord(kind, host, node string, location []string, raw json.RawMessage) remoteRecord {
	r := remoteRecord{Node: node, Host: host, Region: remoteRegion(node, location), Method: strings.ToUpper(kind), Status: remoteWaiting}
	if len(raw) == 0 {
		return r
	}
	r.Status = remoteRequestFailed
	var addresses []string
	if kind == "dns" {
		var answers []struct {
			A, AAAA []string
			Error   string
		}
		if json.Unmarshal(raw, &answers) == nil && len(answers) > 0 {
			r.Status = remoteNoAddress
			for _, answer := range answers {
				addresses = append(addresses, answer.A...)
				addresses = append(addresses, answer.AAAA...)
				if answer.Error != "" {
					r.Status, r.Detail = remoteRequestFailed, answer.Error
				}
			}
		}
	} else {
		var answers [][]any
		if json.Unmarshal(raw, &answers) == nil && len(answers) > 0 && len(answers[0]) >= 4 {
			answer := answers[0]
			if answer[2] != nil {
				r.Detail = fmt.Sprint(answer[2])
			}
			if answer[3] != nil {
				r.Status = fmt.Sprint(answer[3])
			}
			if len(answer) >= 5 {
				if ip, ok := answer[4].(string); ok {
					addresses = append(addresses, ip)
				}
			}
		}
	}
	for _, address := range addresses {
		if ip := publicAddress(address); ip != "" && !slices.Contains(r.IPs, ip) {
			r.IPs = append(r.IPs, ip)
		}
	}
	if kind == "dns" && len(r.IPs) > 0 {
		r.Status = "已返回"
	}
	return r
}

func (c checkHostClient) probeHost(ctx context.Context, u *url.URL, list probeListing, notify func(string), update func([]remoteRecord)) {
	nodes, regions := listedNodes(list)
	if len(nodes) == 0 {
		notify("没有在线地区探针，继续使用 DNS 候选")
		return
	}
	if regions > 0 {
		notify(fmt.Sprintf("正在探测 %d 个地区的 %d 个节点（HTTP 和 DNS）", regions, len(nodes)))
	} else {
		notify(fmt.Sprintf("正在探测 %d 个节点（HTTP 和 DNS）", len(nodes)))
	}
	public := unsignedPictureURL(u)
	targets := []struct {
		kind string
		remoteTarget
	}{
		{"http", remoteTarget{public.String(), u.Hostname()}},
		{"dns", remoteTarget{u.Hostname(), u.Hostname()}},
	}
	pending := make([]remoteRecord, 0, len(nodes)*len(targets))
	for _, target := range targets {
		for _, node := range nodes {
			pending = append(pending, decodeRemoteRecord(target.kind, target.host, node, list.Nodes[node].Location, nil))
		}
	}
	update(pending)

	var wg sync.WaitGroup
	for _, target := range targets {
		wg.Go(func() {
			rows, err := c.check(ctx, target.kind, target.remoteTarget, nodes, update)
			status := remoteNoResult
			switch {
			case ctx.Err() != nil:
				status = remoteStopped
			case len(rows) == 0 && err != nil:
				status = remoteSubmitFailed
			}
			seen := make(map[string]bool, len(rows))
			var final []remoteRecord
			for _, row := range rows {
				seen[row.Node] = true
				if !remoteReturned(row.Status) {
					final = append(final, row)
				}
			}
			for _, node := range nodes {
				if !seen[node] {
					row := decodeRemoteRecord(target.kind, target.host, node, list.Nodes[node].Location, nil)
					row.Status = status
					final = append(final, row)
				}
			}
			if len(final) > 0 {
				update(final)
			}
			method := strings.ToUpper(target.kind)
			if err != nil && ctx.Err() == nil {
				notify(fmt.Sprintf("远程 %s 未完成：%v", method, err))
			} else if ctx.Err() == nil {
				notify(fmt.Sprintf("远程 %s 已返回 %d/%d 个节点记录", method, len(nodes)-len(final), len(nodes)))
			}
		})
	}
	wg.Wait()
}

func newDiscovery(runContext context.Context) findEdges {
	client := checkHostClient{baseURL: checkHost, pollInterval: 1700 * time.Millisecond}
	loadListing := sync.OnceValues(func() (probeListing, error) {
		var list probeListing
		err := client.read(runContext, "/nodes/hosts", &list)
		return list, err
	})
	return func(ctx context.Context, u *url.URL, regional bool, notify func(string), update func([]remoteRecord), publish func([]edge), remoteDone func()) {
		var wg sync.WaitGroup
		wg.Go(func() { ecsLookup(ctx, u.Hostname(), subnetHints, publish) })
		if regional {
			wg.Go(func() {
				defer remoteDone()
				list, err := loadListing()
				if err != nil {
					if ctx.Err() == nil {
						notify("Check-Host 暂不可用，继续使用 DNS 候选")
					}
					return
				}
				client.probeHost(ctx, u, list, notify, func(rows []remoteRecord) {
					update(rows)
					var edges []edge
					for _, row := range rows {
						for _, ip := range row.IPs {
							edges = append(edges, edge{ip, "Check-Host " + row.Method + " · " + row.Region})
						}
					}
					if len(edges) > 0 {
						publish(edges)
					}
				})
			})
		}
		wg.Wait()
	}
}
