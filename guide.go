package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

func (a *app) guide(c *ui.Context) {
	ui.Column(c).FillWidth().Shrink(0).Gap(4).Children(func() {
		heading(c, "技术与维护", 30)
	})
	ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Gap(14).Children(func() {
			ui.Column(c).FillWidth().Padding(14, 14).Gap(5).Radius(6).Background(ui.Hex("#e1ebfc")).Children(func() {
				text(c, "地区记录只是线索").FontWeight(600).TextColor(ui.Hex("#2351ba"))
				caption(c, "Check-Host 与 DoH ECS 用于发现候选 IP。应用在本机验证响应并解码图片，按 SHA-256 去重。").
					FillWidth().Wrap().TextColor(ui.Hex("#2351ba"))
			})
			a.guideFlow(c)
			ui.Row(c).FillWidth().Gap(14).AlignItems(ui.Stretch).Children(func() {
				ui.Column(c).WidthPercent(37).Shrink(0).Children(func() { a.guideParameters(c) })
				ui.Column(c).Grow(1).MinWidth(0).Children(func() { a.guideMaintenance(c) })
			})
		})
	})
}

func (a *app) guideFlow(c *ui.Context) {
	panel(c).Children(func() {
		panelHeader(c, "探测流程", nil)
		ui.Row(c).FillWidth().Padding(16, 0).MinHeight(155).AlignItems(ui.Stretch).Children(func() {
			steps := []struct{ name, detail, file string }{
				{"解析链接", "读取微博详情或图片直链，并收集可用的备用地址。", "network.go"},
				{"发现节点", "通过 DoH 和可选的 Check-Host 查找 CDN 地址，同时验证默认路由。", "network.go"},
				{"验证图片", "保留 Host 和 TLS 验证，识别占位图、拒绝和超时。", "recovery.go"},
				{"保存预览", "解码图片并按 SHA-256 去重，生成图片预览。", "recovery.go"},
			}
			for i, step := range steps {
				column := ui.Column(c).Grow(1).Width(0).MinWidth(0).Padding(0, 14).Gap(6)
				if i > 0 {
					column.BorderWidth(0, 0, 0, 1).BorderColor(stroke)
				}
				column.Children(func() {
					heading(c, step.name, 15)
					caption(c, step.detail).FillWidth().Wrap()
					ui.Spacer(c)
					caption(c, step.file).FontSize(11).AlignSelf(ui.Start).Background(recessed).Padding(3, 5).Radius(3)
				})
			}
		})
	})
}

func (a *app) guideParameters(c *ui.Context) {
	panel(c).Grow(1).Children(func() {
		panelHeader(c, "运行参数", nil)
		for _, row := range []struct{ title, detail string }{
			{"地区发现", a.discoveryMode()},
			{"DoH 地区提示", fmt.Sprintf("%d 个", len(subnetHints)-1)},
			{"探测并发上限", fmt.Sprint(probeConcurrency)},
			{"完整图片下载并发", fmt.Sprint(downloadConcurrency)},
			{"单节点探测超时", probeTimeout.String()},
			{"图床域名", "*.sinaimg.cn / *.sinaimg.com"},
			{"下载超时", downloadTimeout.String()},
		} {
			ui.Row(c).FillWidth().Padding(10, 13).Gap(8).AlignItems(ui.Center).
				BorderWidth(0, 0, 1, 0).BorderColor(stroke).Children(func() {
				caption(c, row.title)
				ui.Spacer(c)
				caption(c, row.detail).FontSize(11).TextColor(primary).Shrink(1).NoWrap().Ellipsis("…").Tooltip(row.detail)
			})
		}
	})
}

func (a *app) guideMaintenance(c *ui.Context) {
	panel(c).Grow(1).Children(func() {
		panelHeader(c, "维护入口", nil)
		for i, row := range []struct{ title, detail, file string }{
			{"地区记录变少", "检查 Check-Host 返回字段、限流和 DoH 结果，也可关闭多地区模式排查。", "network.go"},
			{"占位图误判或大图缺失", "核对占位样本、图片解码和备用尺寸，运行 CGO_ENABLED=0 go test ./... 验证。", "recovery.go"},
			{"微博详情无图片", "微博访客接口可能要求登录。用浏览器打开微博并复制图片直链。", ""},
		} {
			line := ui.Column(c).FillWidth().Padding(13, 13).Gap(6)
			if i > 0 {
				line.BorderWidth(1, 0, 0, 0).BorderColor(stroke)
			}
			line.Children(func() {
				ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
					text(c, row.title).FontSize(13).FontWeight(600)
					ui.Spacer(c)
					if row.file == "" {
						textButton(c, "去恢复页").Height(22).OnClick(func() { a.pane = recoveryPane })
					} else {
						caption(c, row.file).FontSize(11)
					}
				})
				caption(c, row.detail).FillWidth().Wrap()
			})
		}
	})
}
