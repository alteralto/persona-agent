// Package worldmap draws what the bot has seen as one standalone HTML page: the ground it
// surveyed block by block (a PNG inside the page), home, its finds and signs, and the flights it
// flew. Nothing to install to open it; it can be sent or published as it is.
package worldmap

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/png"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/alteralto/persona-agent/internal/explore"
)

var kindColor = map[byte]color.RGBA{
	explore.KindWater:  {63, 118, 228, 255},
	explore.KindLava:   {214, 92, 30, 255},
	explore.KindSand:   {219, 207, 160, 255},
	explore.KindSnow:   {238, 244, 248, 255},
	explore.KindLeaves: {54, 112, 40, 255},
	explore.KindGrass:  {118, 168, 74, 255},
	explore.KindStone:  {138, 138, 138, 255},
	explore.KindPath:   {181, 154, 99, 255},
	explore.KindBuilt:  {166, 92, 52, 255},
	explore.KindOther:  {143, 174, 95, 255},
}

var trackColor = map[string]string{
	"normal": "#c0392b",
	"low":    "#e67e22",
	"tricks": "#8e44ad",
	"route":  "#1f6fb2",
}

var styleName = map[string]string{
	"normal": "обычный полёт",
	"low":    "бреющий",
	"tricks": "пилотаж",
	"route":  "облёт",
}

// Render draws the map page. now is printed as the time of the snapshot.
func Render(p *explore.Places, now time.Time) ([]byte, error) {
	minX, minZ, maxX, maxZ := math.MaxInt, math.MaxInt, math.MinInt, math.MinInt
	grow := func(x, z int) {
		minX, minZ = min(minX, x), min(minZ, z)
		maxX, maxZ = max(maxX, x), max(maxZ, z)
	}
	for _, c := range p.SurfaceChunks() {
		grow(c.X*16, c.Z*16)
		grow(c.X*16+15, c.Z*16+15)
	}
	for _, pl := range p.List {
		grow(pl.X, pl.Z)
	}
	if p.Home != nil {
		grow(explore.Block(p.Home.X), explore.Block(p.Home.Z))
	}
	for _, t := range p.Tracks {
		for _, pt := range t.Points {
			grow(pt[0], pt[1])
		}
	}
	if minX > maxX {
		return nil, fmt.Errorf("nothing seen yet")
	}
	const pad = 24
	minX, minZ, maxX, maxZ = minX-pad, minZ-pad, maxX+pad, maxZ+pad
	w, h := maxX-minX+1, maxZ-minZ+1

	// The ground: one pixel a block column, transparent where never seen.
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for _, c := range p.SurfaceChunks() {
		for dz := 0; dz < 16; dz++ {
			for dx := 0; dx < 16; dx++ {
				x, z := c.X*16+dx, c.Z*16+dz
				if col, ok := kindColor[p.SurfaceAt(x, z)]; ok {
					img.SetRGBA(x-minX, z-minZ, col)
				}
			}
		}
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		return nil, err
	}

	var svg strings.Builder
	fmt.Fprintf(&svg, `<image href="data:image/png;base64,%s" x="%d" y="%d" width="%d" height="%d" style="image-rendering:pixelated"/>`,
		base64.StdEncoding.EncodeToString(pngBuf.Bytes()), minX, minZ, w, h)
	styles := map[string]int{}
	for _, t := range p.Tracks {
		pts := make([]string, len(t.Points))
		for i, pt := range t.Points {
			pts[i] = fmt.Sprintf("%d,%d", pt[0], pt[1])
		}
		st := t.Style
		if trackColor[st] == "" {
			st = "normal"
		}
		styles[st]++
		fmt.Fprintf(&svg, `<polyline class="track" points="%s" stroke="%s"><title>%s, %s</title></polyline>`,
			strings.Join(pts, " "), trackColor[st], styleName[st], t.At.Format("02.01 15:04"))
	}
	places := append([]explore.Place(nil), p.List...)
	sort.Slice(places, func(i, j int) bool { return places[i].Seen.Before(places[j].Seen) })
	for _, pl := range places {
		cls := "find"
		if strings.HasPrefix(pl.Desc, "табличка") {
			cls = "sign"
		}
		fmt.Fprintf(&svg, `<circle class="%s" cx="%d" cy="%d" r="3.2"><title>%s (%d, %d)</title></circle>`,
			cls, pl.X, pl.Z, html.EscapeString(pl.Desc), pl.X, pl.Z)
	}
	if p.Home != nil {
		hx, hz := explore.Block(p.Home.X), explore.Block(p.Home.Z)
		fmt.Fprintf(&svg, `<g class="home" transform="translate(%d %d)"><path d="M-7 1 L0 -7 L7 1 L5 1 L5 7 L-5 7 L-5 1 Z"/><title>Нора Пыхтуна (%d, %d)</title></g>`,
			hx, hz, hx, hz)
	}

	var list strings.Builder
	for i := len(places) - 1; i >= 0 && i >= len(places)-60; i-- {
		pl := places[i]
		fmt.Fprintf(&list, "<li><span>%s</span><b>%d, %d</b></li>", html.EscapeString(pl.Desc), pl.X, pl.Z)
	}
	var legend strings.Builder
	for _, st := range []string{"normal", "low", "tricks", "route"} {
		if styles[st] > 0 {
			fmt.Fprintf(&legend, `<span><i style="background:%s"></i>%s — %d</span>`, trackColor[st], styleName[st], styles[st])
		}
	}
	out := strings.NewReplacer(
		"{{VIEWBOX}}", fmt.Sprintf("%d %d %d %d", minX, minZ, w, h),
		"{{SVG}}", svg.String(),
		"{{CHUNKS}}", fmt.Sprint(len(p.Surface)),
		"{{PLACES}}", fmt.Sprint(len(p.List)),
		"{{FLIGHTS}}", fmt.Sprint(len(p.Tracks)),
		"{{SIZE}}", fmt.Sprintf("%d × %d", w, h),
		"{{WHEN}}", now.Format("02.01.2006 15:04"),
		"{{LIST}}", list.String(),
		"{{LEGEND}}", legend.String(),
	).Replace(page)
	return []byte(out), nil
}

const page = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Карта Пыхтуна</title>
<style>
  :root { --paper: #efe6cf; --ink: #3b2f20; --muted: #7a6a52; --line: #cdbd98; }
  * { box-sizing: border-box; }
  body { margin: 0; background: var(--paper); color: var(--ink); font: 15px/1.45 Georgia, "Times New Roman", serif; }
  header { padding: 18px 20px 6px; }
  h1 { margin: 0; font-size: 26px; letter-spacing: .5px; }
  .sub { color: var(--muted); }
  .stats { display: flex; flex-wrap: wrap; gap: 8px 22px; padding: 6px 20px 10px; }
  .stats b { font-size: 18px; }
  .wrap { display: grid; grid-template-columns: minmax(0, 1fr) 300px; gap: 14px; padding: 0 20px 20px; }
  @media (max-width: 820px) { .wrap { grid-template-columns: 1fr; } }
  .map { position: relative; border: 2px solid var(--line); border-radius: 8px; overflow: hidden;
         background: repeating-linear-gradient(45deg, #e7dcc0 0 6px, #e2d6b8 6px 12px); height: 72vh; }
  svg { width: 100%; height: 100%; display: block; cursor: grab; touch-action: none; }
  svg:active { cursor: grabbing; }
  .track { fill: none; stroke-width: 1.6; stroke-linejoin: round; stroke-linecap: round; opacity: .85; vector-effect: non-scaling-stroke; }
  .find { fill: #f4d03f; stroke: #5b4a1a; stroke-width: 1; vector-effect: non-scaling-stroke; }
  .sign { fill: #fff; stroke: #5b4a1a; stroke-width: 1; vector-effect: non-scaling-stroke; }
  .home path { fill: #c0392b; stroke: #fff; stroke-width: 1.2; vector-effect: non-scaling-stroke; }
  .legend { display: flex; flex-wrap: wrap; gap: 6px 16px; padding: 8px 2px 0; color: var(--muted); font-size: 13px; }
  .legend i { display: inline-block; width: 18px; height: 4px; border-radius: 2px; margin-right: 6px; vertical-align: middle; }
  .legend .dot { width: 9px; height: 9px; border-radius: 50%; border: 1px solid #5b4a1a; }
  aside { border: 2px solid var(--line); border-radius: 8px; padding: 10px 12px; max-height: 72vh; overflow: auto; }
  aside h2 { font-size: 16px; margin: 2px 0 8px; }
  ul { list-style: none; margin: 0; padding: 0; }
  li { display: flex; justify-content: space-between; gap: 10px; padding: 5px 0; border-bottom: 1px dashed var(--line); font-size: 13.5px; }
  li b { white-space: nowrap; color: var(--muted); font-weight: normal; font-family: Consolas, monospace; font-size: 12.5px; }
  .hint { color: var(--muted); font-size: 12.5px; padding: 6px 2px 0; }
</style>
<header>
  <h1>Карта Пыхтуна</h1>
  <div class="sub">Что хоббит-домосед разведал вокруг своей норы · снимок {{WHEN}}</div>
</header>
<div class="stats">
  <span>Разведано чанков: <b>{{CHUNKS}}</b></span>
  <span>Находок и табличек: <b>{{PLACES}}</b></span>
  <span>Полётов на карте: <b>{{FLIGHTS}}</b></span>
  <span>Область: <b>{{SIZE}}</b> блоков</span>
</div>
<div class="wrap">
  <div>
    <div class="map"><svg id="m" viewBox="{{VIEWBOX}}" preserveAspectRatio="xMidYMid meet">{{SVG}}</svg></div>
    <div class="legend">
      <span><i class="dot" style="background:#f4d03f"></i>находка</span>
      <span><i class="dot" style="background:#fff"></i>табличка</span>
      <span><i style="background:#c0392b;height:9px;width:9px"></i>нора</span>
      {{LEGEND}}
    </div>
    <div class="hint">Колесо — масштаб, перетаскивание — сдвиг, двойной щелчок — весь вид. Север сверху.</div>
  </div>
  <aside><h2>Последние находки</h2><ul>{{LIST}}</ul></aside>
</div>
<script>
(() => {
  const svg = document.getElementById("m");
  const full = svg.getAttribute("viewBox").split(" ").map(Number);
  let vb = full.slice();
  const set = () => svg.setAttribute("viewBox", vb.join(" "));
  const pt = e => { const r = svg.getBoundingClientRect(); const s = Math.max(vb[2] / r.width, vb[3] / r.height);
    return [vb[0] + (e.clientX - r.left - (r.width - vb[2] / s) / 2) * s, vb[1] + (e.clientY - r.top - (r.height - vb[3] / s) / 2) * s]; };
  svg.addEventListener("wheel", e => {
    e.preventDefault();
    const k = e.deltaY < 0 ? 0.8 : 1.25, [x, y] = pt(e);
    const nw = Math.min(full[2] * 1.5, Math.max(40, vb[2] * k)), nh = vb[3] * nw / vb[2];
    vb = [x - (x - vb[0]) * nw / vb[2], y - (y - vb[1]) * nh / vb[3], nw, nh]; set();
  }, { passive: false });
  let drag = null;
  svg.addEventListener("pointerdown", e => { drag = pt(e); svg.setPointerCapture(e.pointerId); });
  svg.addEventListener("pointermove", e => { if (!drag) return; const [x, y] = pt(e); vb[0] -= x - drag[0]; vb[1] -= y - drag[1]; set(); });
  svg.addEventListener("pointerup", () => drag = null);
  svg.addEventListener("dblclick", () => { vb = full.slice(); set(); });
})();
</script>
`
