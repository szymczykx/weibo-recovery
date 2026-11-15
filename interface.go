package main

import (
	"cmp"
	"fmt"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const (
	sidebarWidth   float32 = 232
	workspaceInset float32 = 16
	nodePaneInset  float32 = 8
)

var (
	base      = ui.Hex("#f8f8f8")
	rail      = ui.Hex("#fafafa")
	surface   = ui.Hex("#ffffff")
	recessed  = ui.Hex("#f2f2f2")
	stroke    = ui.Hex("#e8e8e8")
	primary   = ui.Hex("#171717")
	secondary = ui.Hex("#737373")
	blue      = ui.Hex("#0969f5")
	good      = ui.Hex("#008658")
	bad       = ui.Hex("#bf2436")
	warning   = ui.Hex("#a95b00")
	quietDot  = ui.Hex("#9a9a9a")
)

func theme() *ui.Theme {
	t := ui.LightTheme()
	t.Background, t.Surface, t.SurfaceHover, t.SurfacePressed = base, surface, recessed, stroke
	t.Border, t.Text, t.TextMuted = ui.Hex("#d9d9d9"), primary, secondary
	t.Accent, t.AccentHover, t.AccentPressed, t.AccentText = blue, ui.Hex("#075adb"), ui.Hex("#064ebd"), surface
	t.Success, t.Danger, t.Focus = good, bad, blue
	t.Radius, t.FontSize = 8, 14
	return t
}

func text(c *ui.Context, s string) ui.Element       { return ui.Text(c, s).FontSize(14) }
func heading(c *ui.Context, s string, size float32) { ui.Text(c, s).FontSize(size).FontWeight(600) }
func muted(c *ui.Context, s string) ui.Element      { return text(c, s).TextColor(secondary) }
func caption(c *ui.Context, s string) ui.Element    { return muted(c, s).FontSize(12) }

func panel(c *ui.Context) ui.Element {
	return ui.Column(c).FillWidth().MinWidth(0).Radius(8).Background(surface).Border(1, stroke).Clip()
}

func panelHeader(c *ui.Context, title string, trailing func()) {
	ui.Row(c).FillWidth().Height(50).Shrink(0).Padding(0, 14).Gap(7).AlignItems(ui.Center).
		Background(ui.Hex("#f6f6f6")).Children(func() {
		caption(c, title).FontSize(13).FontWeight(600)
		if trailing != nil {
			trailing()
		}
	})
}

func dot(c *ui.Context, color ui.Color) {
	ui.Box(c).Size(7, 7).Shrink(0).Radius(4).Background(color)
}

func statusPill(c *ui.Context, label string, color ui.Color) {
	ui.Row(c).Height(21).Padding(0, 6).Gap(5).AlignItems(ui.Center).
		Radius(10).Border(1, stroke).Background(surface).Children(func() {
		dot(c, color)
		caption(c, label).FontWeight(600)
	})
}

func textButton(c *ui.Context, label string) ui.Element {
	button := ui.Button(c, label).Height(28).FontSize(12).Padding(0, 8).NoShadow().Border(0, stroke).TextColor(ui.Hex("#2351ba"))
	if !button.Hovered() && !button.Focused() {
		button.Background(ui.RGBA(0, 0, 0, 0))
	}
	return button
}

func primaryAction(c *ui.Context, label string) ui.Element {
	button := ui.PrimaryButton(c, label).Border(1, ui.Hex("#035de1"))
	if !button.Hovered() && !button.Pressed() {
		button.Gradient(ui.Hex("#2f7df8"), ui.Hex("#0768f6"), 180)
	}
	return button
}

func (a *app) view(c *ui.Context) {
	c.SetTheme(theme())
	services := c.Services()
	c.OnWindowShortcut(ui.Cmd, ui.KeyV, func() {
		if a.pane == recoveryPane && !a.viewerOpen {
			a.paste(services)
		}
	})
	bar := c.TitleBar()
	ui.Column(c).Fill().Background(base).Children(func() {
		ui.Row(c).FillWidth().Height(max(44, bar.Height)).Shrink(0).AlignItems(ui.Center).Justify(ui.End).
			Padding(0, 18+bar.Right, 0, 18+bar.Left).
			BorderWidth(0, 0, 1, 0).BorderColor(stroke).Background(rail).DragWindow().Children(func() {
			if version := mygo.App.Version(); version != "" {
				caption(c, "v"+version)
			}
		})
		ui.Row(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
			a.sidebar(c)
			ui.Column(c).Grow(1).MinWidth(0).FillHeight().Padding(workspaceInset).Gap(16).Children(func() {
				if a.pane == guidePane {
					a.guide(c)
				} else {
					a.workspace(c)
				}
			})
		})
	})
	a.imageViewer(c)
}

func (a *app) sidebar(c *ui.Context) {
	ui.Column(c).Width(sidebarWidth).Shrink(0).FillHeight().Padding(workspaceInset).Gap(19).Background(rail).
		BorderWidth(0, 1, 0, 0).BorderColor(stroke).Children(func() {
		ui.Column(c).FillWidth().Gap(4).Children(func() {
			for _, nav := range []struct {
				name pane
				icon *ui.SVG
			}{{recoveryPane, recoveryIcon}, {guidePane, settingsIcon}} {
				c.Key(nav.name)
				button := ui.Button(c, "").FillWidth().Height(36).Padding(0, 10).Label(string(nav.name)).
					Justify(ui.Start).Border(0, stroke).NoShadow().OnClick(func() { a.pane = nav.name })
				color := secondary
				if a.pane == nav.name {
					button.Background(recessed)
					color = blue
				} else if !button.Hovered() {
					button.Background(rail)
				}
				button.Children(func() {
					ui.Row(c).Gap(9).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, nav.icon).Size(16, 16).TextColor(color)
						label := text(c, string(nav.name)).TextColor(secondary)
						if a.pane == nav.name {
							label.TextColor(primary).FontWeight(600)
						}
					})
				})
			}
		})
		sidebarSection(c, "运行环境", func() {
			environmentRow(c, "网络", "本机直连")
			environmentRow(c, "地区发现", a.discoveryMode())
			environmentRow(c, "并发上限", fmt.Sprint(probeConcurrency))
		})
		sidebarSection(c, "判定图例", func() {
			for _, item := range []struct {
				label, detail string
				color         ui.Color
			}{
				{"命中", "图片候选", good}, {"占位", "图床默认图片", quietDot},
				{"拒绝", "HTTP 401 / 403", bad}, {"超时", "请求超过时限", warning},
			} {
				ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
					dot(c, item.color)
					caption(c, item.label).TextColor(primary)
					ui.Spacer(c)
					caption(c, item.detail)
				})
			}
		})
		ui.Column(c).FillWidth().Padding(10, 11).Gap(4).Radius(6).Background(recessed).Children(func() {
			caption(c, "缓存窗口有限").TextColor(primary).FontWeight(600)
			caption(c, "图片失效后尽早探测。CDN 缓存刷新后可能无法恢复。").FillWidth().Wrap()
		})
	})
}

func sidebarSection(c *ui.Context, title string, render func()) {
	ui.Column(c).FillWidth().Padding(14, 0, 0, 0).Gap(8).
		BorderWidth(1, 0, 0, 0).BorderColor(stroke).Children(func() {
		caption(c, title).FontWeight(600)
		render()
	})
}

func environmentRow(c *ui.Context, label, value string) {
	ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
		caption(c, label)
		ui.Spacer(c)
		caption(c, value).TextColor(primary).FontWeight(600)
	})
}

func (a *app) discoveryMode() string {
	if a.remote {
		return "Check-Host / DoH"
	}
	return "DoH"
}

func (a *app) workspace(c *ui.Context) {
	a.recoveryInput(c)
	a.runStatus(c)

	if !a.showNodes {
		ui.Row(c).Key("collapsed-table").FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			heading(c, "节点明细", 15)
			caption(c, fmt.Sprintf("%d 条本机记录", len(a.attempts)))
			ui.Spacer(c)
			ui.Button(c, "展开节点表").Height(28).OnClick(func() { a.showNodes = true })
		})
		a.preview(c)
		return
	}
	c.Key("recovery-split")
	ui.SplitPercent(c, &a.tablePercent, func() {
		ui.Column(c).Fill().Padding(0, nodePaneInset, 0, 0).Children(func() { a.nodeLists(c) })
	}, func() {
		ui.Column(c).Fill().Padding(0, 0, 0, 6).Children(func() { a.preview(c) })
	}).FillWidth().Grow(1).MinHeight(0).SplitReset(defaultTablePercent).OnChange(func() {
		a.tablePercent = max(36, min(74, a.tablePercent))
	})
}

func (a *app) recoveryInput(c *ui.Context) {
	panel(c).Padding(15, 14).Gap(9).Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			text(c, "微博链接或图片地址").FontWeight(600).IgnoreInput(true)
			ui.Spacer(c)
			label, color := a.recoveryState()
			statusPill(c, label, color)
		})
		ui.Row(c).Gap(7).FillWidth().AlignItems(ui.Center).Children(func() {
			field := ui.Row(c).Grow(1).MinWidth(0).AlignItems(ui.Center).Height(36).
				Background(surface).Border(1, stroke).Radius(6)
			field.Children(func() {
				ui.Box(c).Width(34).FillHeight().Center().BorderWidth(0, 1, 0, 0).BorderColor(stroke).Children(func() {
					ui.Icon(c, linkIcon).Size(16, 16).TextColor(secondary)
				})
				input := ui.TextInput(c, &a.input).Label("微博链接或图片地址").Placeholder("https://weibo.com/… 或 https://wx3.sinaimg.cn/…").
					ReadOnly(a.working).Grow(1).MinWidth(0).Height(34).NoShadow().Border(0, stroke).Background(surface).
					OnChange(func() { a.inputError = "" })
				if input.Focused() {
					field.BorderColor(blue)
				}
			})
			if a.working {
				label := "停止探测"
				if a.runState == runStopping {
					label = "正在停止"
				}
				ui.Button(c, label).Width(90).Height(36).Disabled(a.runState == runStopping).OnClick(a.stop)
			} else {
				primaryAction(c, "开始探测").Width(90).Height(36).Disabled(a.saving).OnClick(a.start)
			}
		})
		if a.inputError != "" {
			caption(c, a.inputError).FillWidth().TextColor(bad).Wrap()
		}
		ui.Row(c).FillWidth().Gap(9).AlignItems(ui.Center).Children(func() {
			ui.Switch(c, &a.remote).Label("多地区探测").Disabled(a.working)
			text(c, "多地区探测").FontSize(13)
			if a.remote {
				caption(c, "Check-Host 接收图片域名和路径")
			} else {
				caption(c, "通过 DoH 发现候选节点")
			}
		})
	})
}

func (a *app) recoveryState() (string, ui.Color) {
	switch {
	case a.runState == runStopping:
		return string(runStopping), quietDot
	case a.working:
		return "探测中", blue
	case a.runState == runStopped:
		return string(runStopped), quietDot
	case a.runState == runFailed:
		return string(runFailed), bad
	case a.runState == runEmpty:
		return string(runEmpty), warning
	case len(a.images) > 0:
		return "已恢复", good
	default:
		return "就绪", quietDot
	}
}

func (a *app) runStatus(c *ui.Context) {
	if a.runState == runFailed || a.runState == runEmpty {
		color, background := bad, ui.Hex("#fbe9eb")
		if a.runState == runEmpty {
			color, background = warning, ui.Hex("#fff3e3")
		}
		ui.Row(c).FillWidth().Shrink(0).Padding(7, 11).Radius(5).Gap(9).AlignItems(ui.Center).Background(background).Children(func() {
			dot(c, color)
			text(c, a.status).Grow(1).MinWidth(0).FontSize(13).TextColor(color).Wrap()
			if a.elapsed > 0 {
				caption(c, fmt.Sprintf("%d 次验证 · %.1f s", len(a.attempts), a.elapsed.Seconds())).TextColor(color)
			}
		})
		return
	}
	label, color := a.recoveryState()
	switch label {
	case "就绪":
		label = "等待链接"
	case "已恢复":
		label = "探测完成"
	}
	detail := a.status
	if detail == "等待链接" {
		detail = "粘贴链接后点击「开始探测」。"
	}
	ui.Row(c).FillWidth().Height(24).Gap(9).AlignItems(ui.Center).Children(func() {
		dot(c, color)
		text(c, label).FontSize(13).FontWeight(600)
		ui.Box(c).Grow(1).MinWidth(0).Clip().Children(func() {
			caption(c, detail).FillWidth().NoWrap().Ellipsis("…").Tooltip(detail)
		})
		for _, metric := range []string{
			fmt.Sprintf("%d 已验证", len(a.attempts)),
			fmt.Sprintf("%d 命中", a.hits),
			fmt.Sprintf("%d 占位", a.placeholders),
		} {
			caption(c, metric)
		}
		if !a.working && a.elapsed > 0 {
			caption(c, fmt.Sprintf("%.1f s", a.elapsed.Seconds()))
		}
	})
}

func emptyPanel(c *ui.Context, title, description string) {
	ui.Column(c).Grow(1).MinHeight(0).FillWidth().Center().Padding(20).Children(func() {
		ui.Column(c).FillWidth().MaxWidth(320).Gap(6).AlignItems(ui.Center).Children(func() {
			text(c, title).FontWeight(600).FillWidth().Wrap().TextAlign(ui.Center)
			if description != "" {
				caption(c, description).FillWidth().Wrap().TextAlign(ui.Center)
			}
		})
	})
}

func (a *app) preview(c *ui.Context) {
	panel(c).Key("preview").Grow(1).MinHeight(0).Children(func() {
		img, bitmap := a.previewSelection()
		row := a.detail
		nodeTitle, nodeNote := "", ""
		if row != nil {
			nodeTitle, _ = outcomeText(row.Outcome)
			nodeNote = row.Detail
			if row.Outcome == "hit" && img == nil {
				nodeTitle = "等待节点图片"
			}
			if a.previewRequest != nil && img == nil {
				nodeNote = "正在读取所选节点的图片。"
			} else if a.nodeImageError != "" && img == nil {
				nodeNote = a.nodeImageError
				if row.Outcome == "hit" {
					nodeTitle = "无法预览节点图片"
				}
			}
		}
		panelHeader(c, "图片预览", func() {
			ui.Spacer(c)
			if len(a.images) > 1 || row != nil && len(a.images) > 0 && img == nil && !a.working {
				a.downloadedImages(c, img, row == nil)
				return
			}
			switch {
			case row != nil && row.Outcome != "hit":
				label, color := outcomeText(row.Outcome)
				statusPill(c, label, color)
			case img != nil:
				statusPill(c, "已校验", good)
			case a.working:
				statusPill(c, "验证中", blue)
			case a.runState == runEmpty:
				statusPill(c, "无候选", warning)
			default:
				statusPill(c, "暂无", quietDot)
			}
		})
		if img == nil {
			title, description := "暂无预览", "点击节点可查看对应图片"
			if a.runState == runEmpty {
				title, description = "未找到可恢复的图片", ""
			} else if a.working {
				title, description = "等待图片结果", ""
			}
			if row != nil {
				title = nodeTitle
				description = strings.Trim(strings.Join([]string{nodeNote, row.Source, fallbackIP(row.IP)}, " · "), " ·")
			}
			emptyPanel(c, title, description)
			a.saveFeedback(c)
			return
		}
		image := *img
		ui.Column(c).FillWidth().Grow(1).MinHeight(0).Padding(12, 14, 14, 14).Gap(7).Children(func() {
			frame := ui.Column(c).Grow(1).FillWidth().MinHeight(0).Background(recessed).Radius(6).Clip().Center()
			if bitmap != nil {
				frame.Cursor(ui.CursorPointer).Label("放大当前图片").OnClick(func() { a.openViewer(image, bitmap) })
			}
			frame.Children(func() {
				switch {
				case bitmap != nil:
					ui.Image(c, bitmap).Fill().Fit(ui.Contain)
				case a.previewRequest != nil:
					caption(c, "正在加载预览")
				case a.nodeImageError != "":
					caption(c, a.nodeImageError).Padding(8).Wrap()
				default:
					caption(c, "保存后可用系统预览打开")
				}
			})
			ui.Row(c).FillWidth().Gap(5).AlignItems(ui.Center).Children(func() {
				text(c, fmt.Sprintf("%d × %d", img.Width, img.Height)).FontWeight(650).NoWrap()
				ui.Spacer(c)
				format := strings.ToUpper(strings.TrimPrefix(filepath.Ext(img.Name), "."))
				caption(c, strings.TrimPrefix(format+" · "+formatBytes(int64(img.Bytes)), " · ")).NoWrap()
			})
			if row == nil || row.Outcome == "hit" {
				caption(c, imageVersion(*img)+" · "+img.Variant).NoWrap().Ellipsis("…")
			}
			source := strings.TrimPrefix(img.Source+" · "+fallbackIP(img.IP), " · ")
			previewMetadata(c, "下载来源："+source, source+" · "+img.URL)
			if img.SHA256 != "" {
				hash := img.SHA256
				if len(hash) > 16 {
					hash = hash[:10] + "…" + hash[len(hash)-4:]
				}
				previewMetadata(c, "SHA-256 "+hash, img.SHA256)
			}
			if row == nil || row.Outcome == "hit" {
				var data []byte
				if img.Path == "" {
					data = a.nodeImageData
				}
				if img.Path != "" || data != nil {
					primaryAction(c, "保存图片…").Height(33).FillWidth().Disabled(a.saving).OnClick(func() { a.save(image, data) })
				}
			}
			if bitmap != nil || img.Path != "" && a.previewRequest == nil {
				ui.Button(c, "查看大图").Height(30).FillWidth().OnClick(func() { a.openViewer(image, bitmap) })
			}
			a.saveFeedback(c)
		})
	})
}

func (a *app) downloadedImages(c *ui.Context, current *recovered, showingSaved bool) {
	ui.MenuButton(c, "已恢复图片", func(menu *ui.Menu) {
		for _, img := range orderedImages(a.images) {
			name := fmt.Sprintf("%s · %s · %d × %d · %s", imageVersion(img), img.Variant, img.Width, img.Height, img.SHA256[:min(8, len(img.SHA256))])
			if menu.Item(name).Checked(showingSaved && current != nil && img.SHA256 == current.SHA256).Chosen() {
				a.manualImage = true
				a.chooseImage(img)
			}
		}
	}).Label("选择已恢复图片").Height(26)
}

func (a *app) saveFeedback(c *ui.Context) {
	if a.saveStatus == "" {
		return
	}
	color := good
	if a.saveProblem {
		color = bad
	}
	caption(c, a.saveStatus).FillWidth().Padding(0, 8).TextColor(color).Wrap()
}

func previewMetadata(c *ui.Context, label, detail string) {
	ui.Box(c).FillWidth().Clip().Children(func() {
		caption(c, label).FillWidth().NoWrap().Ellipsis("…").Tooltip(detail)
	})
}

func imageVersion(img recovered) string {
	if img.Original {
		return "微博标记原图"
	}
	if isThumbnailVariant(img.Variant) {
		return "缩略图"
	}
	switch img.Variant {
	case "large", "largest", "original", "orj1080", "orj960", "mw2000":
		return "大图"
	default:
		return "图片"
	}
}

func (a *app) imageViewer(c *ui.Context) {
	dialog := ui.DialogBase(c, &a.viewerOpen, func(backdrop, dialog ui.Element) {
		img := a.viewerImage
		backdrop.Background(ui.RGBA(22, 24, 28, 0.55))
		dialog.WidthPercent(92).HeightPercent(92).Background(surface).Radius(12).Border(1, stroke).Clip()
		ui.Column(c).Fill().Padding(16).Gap(12).Children(func() {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				heading(c, "查看大图", 16)
				muted(c, fmt.Sprintf("%s · %d × %d · %s", imageVersion(img), img.Width, img.Height, img.Variant))
				ui.Spacer(c)
				if a.viewerBitmap != nil {
					zoom := "原始像素"
					if a.viewerActualSize {
						zoom = "适合窗口"
					}
					ui.Button(c, zoom).Height(30).OnClick(func() {
						a.viewerActualSize = !a.viewerActualSize
						a.viewerScroll.X, a.viewerScroll.Y = 0, 0
					})
				}
				ui.Button(c, "关闭").Height(30).OnClick(a.closeViewer)
			})
			if bitmap := a.viewerBitmap; bitmap != nil {
				if a.viewerActualSize {
					ui.ScrollBoth(c).FillWidth().Grow(1).MinHeight(0).Background(recessed).
						TrackScroll(&a.viewerScroll).Children(func() {
						ui.Image(c, bitmap).Size(float32(img.Width), float32(img.Height)).Shrink(0)
					})
				} else {
					ui.Box(c).FillWidth().Grow(1).MinHeight(0).Background(recessed).Clip().Children(func() {
						ui.Image(c, bitmap).Fill().Fit(ui.Contain)
					})
				}
			} else {
				ui.Column(c).FillWidth().Grow(1).Center().Gap(8).Children(func() {
					muted(c, "可用系统预览打开已验证的图片。")
					ui.Button(c, "在系统预览中打开").OnClick(func() {
						if err := mygo.Shell.OpenPath(img.Path); err != nil {
							a.saveStatus, a.saveProblem = "打开失败："+err.Error(), true
							a.closeViewer()
						}
					})
				})
			}
		})
	})
	if dialog.Dismissed() {
		a.closeViewer()
	}
}

func fallbackIP(ip string) string {
	if ip == "" {
		return "默认路由"
	}
	return ip
}

func formatBytes(size int64) string {
	switch {
	case size < 0:
		return "—"
	case size < 1024:
		return fmt.Sprintf("%d B", size)
	case size < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(size)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(size)/(1<<20))
	}
}

func outcomeText(outcome string) (string, ui.Color) {
	switch outcome {
	case "hit":
		return "图片候选", good
	case "placeholder":
		return "占位图", secondary
	case "forbidden":
		return "拒绝访问", bad
	case "missing":
		return "无图片", secondary
	case "not_image":
		return "非图片响应", secondary
	case "timeout":
		return "超时", warning
	default:
		return "请求错误", bad
	}
}

func filterMatches(filter string, row attempt) bool {
	switch filter {
	case "全部":
		return true
	case "图片候选":
		return row.Outcome == "hit"
	case "占位图":
		return row.Outcome == "placeholder"
	case "拒绝访问":
		return row.Outcome == "forbidden"
	case "超时":
		return row.Outcome == "timeout"
	case "其他":
		return row.Outcome != "hit" && row.Outcome != "placeholder" && row.Outcome != "forbidden" && row.Outcome != "timeout"
	default:
		return false
	}
}

var nodeFilters = []string{"全部", "图片候选", "占位图", "拒绝访问", "超时", "其他", "运行日志"}

func (a *app) resetNodeView() {
	a.detailVisible, a.revealNode = false, false
	a.nodeHorizontal.X, a.nodeScroll.Y = 0, 0
}

func (a *app) nodeLists(c *ui.Context) {
	rows := a.regions
	if a.remoteCollapsed || (!a.remote && len(rows) == 0) {
		ui.Column(c).Fill().MinHeight(0).Gap(8).Children(func() {
			a.remotePanel(c, rows, true)
			a.nodePanel(c)
		})
		return
	}

	c.Key("node-lists-split")
	ui.SplitVerticalPercent(c, &a.remotePercent, func() {
		ui.Column(c).Fill().MinHeight(0).Padding(0, 0, 4, 0).Children(func() {
			a.remotePanel(c, rows, false)
		})
	}, func() {
		ui.Column(c).Fill().MinHeight(0).Padding(4, 0, 0, 0).Children(func() {
			a.nodePanel(c)
		})
	}).Fill().MinHeight(0).SplitReset(defaultRemotePercent).OnChange(func() {
		a.remotePercent = max(20, min(55, a.remotePercent))
	})
}

func (a *app) remotePanel(c *ui.Context, rows []remoteProbeRow, collapsed bool) {
	card := panel(c).Key("remote-panel").MinHeight(0)
	if !collapsed {
		card.Grow(1)
	}
	card.Children(func() {
		panelHeader(c, "远程探测", func() {
			label, color := a.remotePhase()
			statusPill(c, label, color)
			ui.Box(c).MinWidth(0).Clip().Shrink(1).Children(func() {
				if len(rows) > 0 {
					caption(c, a.remoteCount()).FillWidth().NoWrap().Ellipsis("…")
				}
			})
			ui.Spacer(c)
			if !collapsed {
				textButton(c, "折叠").Label("折叠远程探测").Shrink(0).OnClick(func() { a.remoteCollapsed = true })
			} else if a.remote || len(rows) > 0 {
				textButton(c, "展开").Label("展开远程探测").Shrink(0).OnClick(func() { a.remoteCollapsed = false })
			}
		})
		if !collapsed {
			a.remoteTable(c, rows)
		}
	})
}

func (a *app) nodePanel(c *ui.Context) {
	panel(c).Key("node-panel").Grow(1).MinHeight(0).Children(func() {
		panelHeader(c, "节点明细", func() {
			label, color := a.localPhase()
			statusPill(c, label, color)
			ui.Box(c).MinWidth(0).Clip().Shrink(1).Children(func() {
				count := a.rowCount()
				if a.working && a.total > 0 && a.filter == "全部" {
					count = fmt.Sprintf("%d / %d 已验证", a.done, a.total)
				}
				caption(c, count).FillWidth().NoWrap().Ellipsis("…")
			})
			ui.Spacer(c)
			textButton(c, "收起").Label("收起节点区").Shrink(0).OnClick(func() { a.showNodes = false })
		})
		if a.working && a.total > 0 {
			ui.Progress(c, float64(a.done)/float64(a.total)).FillWidth().Height(3).Shrink(0)
		}
		if len(a.attempts) > 0 || len(a.regions) > 0 || len(a.activity) > 0 || a.filter != "全部" {
			a.nodeToolbar(c)
		}
		switch a.filter {
		case "运行日志":
			c.Key("activity-scroll")
			ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).TrackScroll(&a.activityScroll).Children(func() {
				for _, message := range a.activity {
					muted(c, message).FillWidth().Padding(7, 12).Wrap()
				}
			})
		default:
			if len(a.attempts) > 0 {
				a.attemptTable(c)
			} else if a.working {
				emptyPanel(c, "等待本机节点返回", "")
			} else {
				emptyPanel(c, "暂无节点记录", "开始探测后，节点结果会陆续出现")
			}
		}
		if a.detailVisible && a.detail != nil {
			a.requestDetail(c, a.detail)
		}
	})
}

func (a *app) nodeToolbar(c *ui.Context) {
	ui.Row(c).FillWidth().Shrink(0).Padding(5, 11).Gap(6).AlignItems(ui.Center).
		BorderWidth(0, 0, 1, 0).BorderColor(stroke).Children(func() {
		ui.ScrollHorizontal(c).Grow(1).MinWidth(0).Height(28).Children(func() {
			ui.Row(c).Shrink(0).Height(27).Gap(2).Padding(2).Radius(6).Background(recessed).Children(func() {
				for _, item := range []struct{ label, value string }{
					{"全部", "全部"}, {"命中", "图片候选"}, {"占位", "占位图"}, {"拒绝", "拒绝访问"}, {"超时", "超时"},
				} {
					c.Key(item.value)
					button := ui.Button(c, item.label).Label("筛选："+item.label).Height(23).Padding(0, 7).Shrink(0).FontSize(12).Border(0, stroke).NoShadow()
					if a.filter == item.value {
						button.Background(surface).Border(1, stroke).FontWeight(600)
					} else if !button.Hovered() {
						button.Background(recessed).TextColor(secondary)
					}
					button.OnClick(func() {
						a.filter = item.value
						a.resetNodeView()
					})
				}
			})
		})
		ui.MenuButton(c, "筛选", func(menu *ui.Menu) {
			for _, filter := range nodeFilters {
				if menu.Item(filter).Checked(a.filter == filter).Chosen() {
					a.filter = filter
					a.resetNodeView()
				}
			}
		}).Label("筛选节点").Height(27).Shrink(0)
	})
}

func (a *app) rowCount() string {
	switch a.filter {
	case "运行日志":
		return fmt.Sprintf("%d 条日志", len(a.activity))
	case "全部":
		return fmt.Sprintf("%d 条记录", len(a.attempts))
	default:
		return fmt.Sprintf("%d / %d 条记录", len(a.sortedAttempts()), len(a.attempts))
	}
}

func (a *app) tableWidth(c *ui.Context, minimum float32) float32 {
	width, _ := c.Size()
	// Match SplitPercent's one-point divider and the node panel's border.
	available := width - sidebarWidth - 2*workspaceInset - 1
	return max(minimum, available*a.tablePercent/100-nodePaneInset-2)
}

func (a *app) changeSort(column string) {
	if a.sortColumn == column {
		a.sortDescending = !a.sortDescending
	} else {
		a.sortColumn = column
		a.sortDescending = column == "尺寸"
	}
	a.nodeScroll.Y = 0
	a.revealNode = a.detail != nil
}

func (a *app) sortedAttempts() []int {
	if a.visibleFilter != a.filter || a.visibleSort != a.sortColumn || a.visibleDescending != a.sortDescending {
		a.visibleAttempts = a.visibleAttempts[:0]
		a.visibleCount = 0
		a.visibleFilter, a.visibleSort, a.visibleDescending = a.filter, a.sortColumn, a.sortDescending
	}
	if a.visibleCount == len(a.attempts) {
		return a.visibleAttempts
	}
	for i := a.visibleCount; i < len(a.attempts); i++ {
		if filterMatches(a.filter, a.attempts[i]) {
			a.visibleAttempts = append(a.visibleAttempts, i)
		}
	}
	a.visibleCount = len(a.attempts)
	if a.sortColumn != "" {
		slices.SortStableFunc(a.visibleAttempts, func(i, j int) int {
			if a.sortDescending {
				i, j = j, i
			}
			return compareAttempts(a.sortColumn, a.attempts[i], a.attempts[j])
		})
	}
	return a.visibleAttempts
}

func compareAttempts(column string, a, b attempt) int {
	switch column {
	case "节点来源":
		return strings.Compare(a.Source, b.Source)
	case "图床":
		return strings.Compare(a.Host, b.Host)
	case "版本":
		return strings.Compare(a.Variant, b.Variant)
	case "节点 IP":
		first, _ := netip.ParseAddr(a.IP)
		second, _ := netip.ParseAddr(b.IP)
		return first.Compare(second)
	case "判定":
		return cmp.Compare(outcomeRank(a.Outcome), outcomeRank(b.Outcome))
	case "HTTP":
		return cmp.Compare(a.HTTP, b.HTTP)
	case "尺寸":
		return cmp.Compare(int64(a.Width)*int64(a.Height), int64(b.Width)*int64(b.Height))
	case "延迟":
		return cmp.Compare(a.Millis, b.Millis)
	case "缓存":
		return strings.Compare(a.Cache, b.Cache)
	default:
		return 0
	}
}

func outcomeRank(outcome string) int {
	switch outcome {
	case "hit":
		return 0
	case "placeholder":
		return 1
	case "forbidden":
		return 2
	case "timeout":
		return 3
	default:
		return 4
	}
}

func (a *app) attemptTable(c *ui.Context) {
	cols := []cell{{"节点来源", 0}, {"节点 IP", 120}, {"图床", 112}, {"版本", 72}, {"判定", 78}, {"HTTP", 52}, {"尺寸", 90}, {"延迟", 73}, {"缓存", 80}}
	rows := a.sortedAttempts()
	a.nodeList.Key = func(i int) any {
		if i == 0 || len(rows) == 0 {
			return -i - 1
		}
		return rows[i-1]
	}
	a.nodeList.Header = func(i int) bool { return i == 0 }
	a.nodeList.KeepOffset = true
	if a.revealNode && a.detail != nil {
		if i := slices.IndexFunc(rows, func(i int) bool { return a.attempts[i] == *a.detail }); i >= 0 {
			a.nodeList.ScrollTo(i+1, ui.Center)
		}
		a.revealNode = false
	}
	a.tableList(c, "attempt-table", cols, 831, &a.nodeScroll, &a.nodeHorizontal, &a.nodeList, len(rows), a.sortColumn, a.sortDescending, a.changeSort, func(i int) {
		if len(rows) == 0 {
			muted(c, "没有匹配的请求。").Padding(14)
			return
		}
		index := rows[i]
		row := a.attempts[index]
		label, color := outcomeText(row.Outcome)
		if row.Outcome == "hit" {
			label = "命中"
		}
		line := ui.Row(c).FillWidth().Height(34).Shrink(0).Padding(0, 12).Gap(7).AlignItems(ui.Center).
			Cursor(ui.CursorPointer).Tooltip(strings.Trim(row.Detail+" · "+row.Cache, " ·")).
			OnClick(func() {
				a.selectNodeWithSample(&a.attempts[index], nil)
				a.detailVisible = true
			})
		if a.detail != nil && *a.detail == row {
			line.Background(ui.Hex("#edf5ff"))
		} else if line.Hovered() {
			line.Background(base)
		}
		line.Children(func() {
			tableText(c, row.Source, cols[0].width, primary)
			tableText(c, fallbackIP(row.IP), cols[1].width, primary)
			tableText(c, row.Host, cols[2].width, secondary)
			tableText(c, row.Variant, cols[3].width, secondary)
			ui.Row(c).Width(cols[4].width).Shrink(0).Gap(4).AlignItems(ui.Center).Children(func() {
				dot(c, color)
				caption(c, label).TextColor(primary).FontSize(11)
			})
			code, dimensions, cache := "—", "—", row.Cache
			if row.HTTP > 0 {
				code = fmt.Sprint(row.HTTP)
			}
			if row.Width > 0 && row.Height > 0 {
				dimensions = fmt.Sprintf("%d × %d", row.Width, row.Height)
			}
			if cache == "" {
				cache = "—"
			}
			tableText(c, code, cols[5].width, secondary)
			tableText(c, dimensions, cols[6].width, secondary)
			tableText(c, fmt.Sprintf("%d ms", row.Millis), cols[7].width, secondary)
			tableText(c, cache, cols[8].width, secondary)
		})
	})
}

func (a *app) tableList(c *ui.Context, key string, cols []cell, minWidth float32, vertical, horizontal *ui.ScrollState, list *ui.ListState, count int, sortColumn string, descending bool, sortBy func(string), row func(int)) {
	c.Key(key)
	ui.ScrollHorizontal(c).FillWidth().Grow(1).MinHeight(0).TrackScroll(horizontal).Children(func() {
		ui.List(c, list, max(1, count)+1, func(i int) {
			if i == 0 {
				header(c, cols, sortColumn, descending, sortBy)
			} else {
				row(i - 1)
			}
		}).Width(a.tableWidth(c, minWidth)).FillHeight().MinHeight(0).Shrink(0).TrackScroll(vertical)
	})
}

func (a *app) requestDetail(c *ui.Context, row *attempt) {
	ui.Column(c).FillWidth().Padding(7, 10).Gap(4).Background(base).
		BorderWidth(1, 0, 0, 0).BorderColor(stroke).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			heading(c, "请求详情 · "+fallbackIP(row.IP), 14)
			ui.Spacer(c)
			ui.Button(c, "关闭详情").Height(27).OnClick(func() { a.detailVisible = false })
		})
		for _, value := range []string{row.Variant + " · " + formatBytes(row.Bytes), row.URL, row.Detail, row.Cache} {
			if value != "" {
				ui.Box(c).FillWidth().Clip().Children(func() {
					muted(c, value).FillWidth().NoWrap().Ellipsis("…").Tooltip(value).Selectable()
				})
			}
		}
	})
}

type cell struct {
	title string
	width float32
}

func tableText(c *ui.Context, value string, width float32, color ui.Color) {
	box := ui.Box(c).MinWidth(0).Clip()
	if width == 0 {
		box.Grow(1)
	} else {
		box.Width(width).Shrink(0)
	}
	box.Children(func() {
		text(c, value).FillWidth().FontSize(12).NoWrap().Ellipsis("…").TextColor(color)
	})
}

func header(c *ui.Context, cols []cell, sortColumn string, descending bool, sortBy func(string)) {
	ui.Row(c).FillWidth().Height(31).Shrink(0).Padding(0, 12).Gap(7).AlignItems(ui.Center).
		Background(base).BorderWidth(0, 0, 1, 0).BorderColor(stroke).Children(func() {
		for _, col := range cols {
			label := col.title
			if sortColumn == col.title {
				if descending {
					label += " ↓"
				} else {
					label += " ↑"
				}
			}
			box := ui.Box(c).MinWidth(0).Height(25)
			if col.width == 0 {
				box.Grow(1)
			} else {
				box.Width(col.width).Shrink(0)
			}
			box.Children(func() {
				button := textButton(c, label).Label("按" + col.title + "排序").FillWidth().Height(25).
					Padding(0).Justify(ui.Start).TextAlign(ui.Start).TextColor(secondary).OnClick(func() {
					sortBy(col.title)
				})
				if col.title == "候选 IP" {
					button.Tooltip("按去重后的候选 IP 数量排序")
				}
				if sortColumn == col.title {
					button.TextColor(primary).FontWeight(600)
				}
			})
		}
	})
}

func (a *app) changeRemoteSort(column string) {
	if a.remoteSortColumn == column {
		a.remoteSortDescending = !a.remoteSortDescending
	} else {
		a.remoteSortColumn = column
		a.remoteSortDescending = false
	}
	a.remoteScroll.Y = 0
	a.remoteRowsChanged = true
}

func (a *app) sortRemoteRows() {
	if !a.remoteRowsChanged {
		return
	}
	a.remoteRowsChanged = false
	if a.remoteSortColumn == "" {
		return
	}
	slices.SortStableFunc(a.regions, func(left, right remoteProbeRow) int {
		if a.remoteSortDescending {
			left, right = right, left
		}
		switch a.remoteSortColumn {
		case "图床":
			return strings.Compare(left.key.host, right.key.host)
		case "地区":
			return strings.Compare(left.region, right.region)
		case "探针":
			return strings.Compare(left.key.node, right.key.node)
		case "HTTP":
			return cmp.Compare(remoteStatusOrder(left.http), remoteStatusOrder(right.http))
		case "DNS":
			return cmp.Compare(remoteStatusOrder(left.dns), remoteStatusOrder(right.dns))
		case "候选 IP":
			return cmp.Compare(len(left.ips), len(right.ips))
		default:
			return 0
		}
	})
	for i, row := range a.regions {
		a.regionIndex[row.key] = i
	}
}

func remoteStatusOrder(record remoteRecord) int {
	switch record.Status {
	case "已返回":
		return 0
	case remoteNoAddress:
		return 600
	case remoteRequestFailed, remoteSubmitFailed:
		return 700
	case remoteStopped, remoteNoResult:
		return 800
	case remoteWaiting, "":
		return 900
	default:
		code, err := strconv.Atoi(record.Status)
		if err == nil && code >= 100 && code <= 599 {
			return code
		}
		return 700
	}
}

func (a *app) remoteCount() string {
	return fmt.Sprintf("HTTP %d/%d · DNS %d/%d", a.remoteCounts.http, len(a.regions), a.remoteCounts.dns, len(a.regions))
}

func remoteStatus(record remoteRecord, working bool) string {
	if record.Status != "" {
		return record.Status
	}
	if working {
		return remoteWaiting
	}
	return "—"
}

func (a *app) remotePhase() (string, ui.Color) {
	switch {
	case a.runState == runStopping && a.remoteLookups > 0:
		return string(runStopping), quietDot
	case a.remoteLookups > 0 && len(a.regions) == 0:
		return "获取探针", blue
	case a.remoteLookups > 0:
		return "探测中", blue
	case a.remoteCounts.stopped > 0:
		return "已停止", quietDot
	case len(a.regions) > 0:
		if a.remoteCounts.http < len(a.regions) || a.remoteCounts.dns < len(a.regions) {
			return "部分未返回", warning
		}
		return "已完成", good
	case !a.remote:
		return "已关闭", quietDot
	case a.remoteFinished && a.runState == runStopped:
		return string(runStopped), quietDot
	case a.remoteFinished:
		return "无远程结果", warning
	default:
		return "待开始", quietDot
	}
}

func (a *app) localPhase() (string, ui.Color) {
	switch {
	case a.runState == runStopping:
		return string(runStopping), quietDot
	case a.working && a.total == 0:
		return "等待候选 IP", quietDot
	case a.working && a.done < a.total:
		return "验证中", blue
	case a.working:
		return "本批已验证", good
	case a.runState == runStopped:
		return "已停止", quietDot
	case len(a.attempts) > 0:
		return "已完成", good
	default:
		return "待开始", quietDot
	}
}

func remoteStatusLabel(c *ui.Context, status string, width float32) {
	label := status
	color, tint := secondary, recessed
	switch {
	case status == "—":
		tableText(c, status, width, secondary)
		return
	case status == remoteWaiting:
		color, tint = ui.Hex("#2351ba"), ui.Hex("#eaf1fc")
	case status == "401" || status == "403":
		label += " 拒绝"
		color, tint = bad, ui.Hex("#fbe9eb")
	case status == remoteNoResult || status == remoteNoAddress:
		color, tint = warning, ui.Hex("#fff3e3")
	case status == remoteSubmitFailed || status == remoteRequestFailed || strings.HasPrefix(status, "5"):
		color, tint = bad, ui.Hex("#fbe9eb")
	case status == "已返回" || len(status) == 3 && status[0] == '2':
		color, tint = good, ui.Hex("#e4f4ed")
	case len(status) == 3:
		color, tint = warning, ui.Hex("#fff3e3")
	}
	ui.Row(c).Width(width).Shrink(0).AlignItems(ui.Center).Children(func() {
		caption(c, label).FontSize(11).FontWeight(600).TextColor(color).
			Padding(2, 5).Radius(4).Background(tint).NoWrap()
	})
}

func (a *app) remoteTable(c *ui.Context, rows []remoteProbeRow) {
	if len(rows) == 0 {
		switch {
		case a.remoteLookups > 0:
			emptyPanel(c, "正在等待远程探针列表", "")
		default:
			emptyPanel(c, "暂无远程探测记录", "")
		}
		return
	}
	cols := []cell{{"图床", 118}, {"地区", 0}, {"探针", 62}, {"HTTP", 79}, {"DNS", 72}, {"候选 IP", 156}}
	a.sortRemoteRows()
	a.remoteList.Key = func(i int) any {
		if i == 0 {
			return "header"
		}
		return rows[i-1].key
	}
	a.remoteList.Header = func(i int) bool { return i == 0 }
	a.remoteList.KeepOffset = true
	a.tableList(c, "remote-table", cols, 659, &a.remoteScroll, &a.remoteHorizontal, &a.remoteList, len(rows), a.remoteSortColumn, a.remoteSortDescending, a.changeRemoteSort, func(i int) {
		row := rows[i]
		addresses := row.addresses
		if addresses == "" {
			addresses = "—"
		}
		node := strings.TrimSuffix(row.key.node, ".node.check-host.net")
		ui.Row(c).FillWidth().Height(34).Shrink(0).Padding(0, 12).Gap(7).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(stroke).
			Tooltip(strings.TrimSpace(strings.Join([]string{row.key.host, row.key.node, row.http.Detail, row.dns.Detail, addresses}, " "))).Children(func() {
			tableText(c, row.key.host, cols[0].width, primary)
			tableText(c, row.region, cols[1].width, primary)
			tableText(c, node, cols[2].width, secondary)
			remoteStatusLabel(c, remoteStatus(row.http, a.working), cols[3].width)
			remoteStatusLabel(c, remoteStatus(row.dns, a.working), cols[4].width)
			tableText(c, addresses, cols[5].width, secondary)
		})
	})
}

// Navigation and link icons from the supplied Kumo design.
var (
	recoveryIcon = designIcon(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="2.6"></circle><path d="M12 3v2.4M12 18.6V21M3 12h2.4M18.6 12H21M5.6 5.6l1.7 1.7M16.7 16.7l1.7 1.7M18.4 5.6l-1.7 1.7M7.3 16.7l-1.7 1.7"></path></svg>`)
	settingsIcon = designIcon(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h10M18 7h2M4 17h4M12 17h8"></path><circle cx="16" cy="7" r="2.2"></circle><circle cx="10" cy="17" r="2.2"></circle></svg>`)
	linkIcon     = designIcon(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M10 13a5 5 0 0 0 7.1 0l2-2a5 5 0 0 0-7.1-7.1l-1 1"></path><path d="M14 11a5 5 0 0 0-7.1 0l-2 2a5 5 0 0 0 7.1 7.1l1-1"></path></svg>`)
)

func designIcon(source string) *ui.SVG {
	icon, err := ui.ParseSVG([]byte(source))
	if err != nil {
		panic(err)
	}
	return icon
}
