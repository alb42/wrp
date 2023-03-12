//
// WRP - Web Rendering Proxy
//
// Copyright (c) 2013-2018 Antoni Sawicki
// Copyright (c) 2019-2022 Google LLC
//

package main

import (
	"bytes"
	"context"
	"embed"
	"flag"
	"fmt"
	"html"
	"html/template"
	"image"
	"image/color/palette"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"io/ioutil"
	"log"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/MaxHalford/halfgone"
	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/css"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/soniakeys/quant/median"
)

const version = "4.6.0"

var (
	addr        = flag.String("l", ":8080", "Listen address:port, default :8080")
	tlsAddr     = flag.String("sl", ":8081", "TLS Listen address:port, default :8081 (requires cert.crt/private.key)")
	headless    = flag.Bool("h", true, "Headless mode / hide browser window (default true)")
	noDel       = flag.Bool("n", false, "Do not free maps and images after use")
	defType     = flag.String("t", "gif", "Image type: png|gif|jpg")
	jpgQual     = flag.Int("q", 80, "Jpeg image quality, default 80%")
	fgeom       = flag.String("g", "1152x600x216", "Geometry: width x height x colors, height can be 0 for unlimited")
	htmFnam     = flag.String("ui", "wrp.html", "HTML template file for the UI")
	delay       = flag.Duration("s", 2*time.Second, "Delay/sleep after page is rendered and before screenshot is taken")
	imgOpti     = flag.Bool("O", false, "Optimize PNG images with external tool (optipng)")
	token       = flag.String("token", "", "If set, all requests need to have this set as Bearer header")
	logTarget   = flag.String("log", "", "If set, logging will go to this file instead of stdout")
	srv         http.Server
	httpsSrv    http.Server
	actx, ctx   context.Context
	acncl, cncl context.CancelFunc
	img         = make(map[string]bytes.Buffer)
	ismap       = make(map[string]wrpReq)
	defGeom     geom
	htmlTmpl    *template.Template
)

//go:embed *.html
var fs embed.FS
var clipcontent []byte

type geom struct {
	w int64
	h int64
	c int64
}

// Data for html template
type uiData struct {
	Version    string
	URL        string
	BgColor    string
	NColors    int64
	Width      int64
	Height     int64
	Zoom       float64
	ImgType    string
	ImgURL     string
	ImgSize    string
	ImgWidth   int
	ImgHeight  int
	MapURL     string
	PageHeight string
	Title      string
	DownURL    string
	DownType   string
}

// Parameters for HTML print function
type printParams struct {
	bgColor    string
	pageHeight string
	imgSize    string
	imgURL     string
	mapURL     string
	imgWidth   int
	imgHeight  int
}

// WRP Request
type wrpReq struct {
	url       string  // url
	width     int64   // width
	height    int64   // height
	minheight int64   // minimum height when height = 0
	zoom      float64 // zoom/scale
	colors    int64   // #colors
	mouseX    int64   // mouseX down
	mouseY    int64   // mouseY down
	mouseX2   int64   // mouseX release
	mouseY2   int64   // mouseY release
	keys      string  // keys to send
	buttons   string  // Fn buttons
	imgType   string  // imgtype
	title     string  // titlepage
	downurl   string
	downtype  string
	w         http.ResponseWriter
	r         *http.Request
}

// Parse HTML Form, Process Input Boxes, Etc.
func (rq *wrpReq) parseForm() {
	rq.r.ParseForm()
	rq.url = rq.r.FormValue("url")
	if len(rq.url) > 1 && !strings.HasPrefix(rq.url, "http") {
		rq.url = fmt.Sprintf("http://www.google.com/search?q=%s", url.QueryEscape(rq.url))
	}
	rq.width, _ = strconv.ParseInt(rq.r.FormValue("w"), 10, 64)
	rq.height, _ = strconv.ParseInt(rq.r.FormValue("h"), 10, 64)
	if rq.width < 10 && rq.height < 10 {
		rq.width = defGeom.w
		rq.height = defGeom.h
	}
	rq.minheight, _ = strconv.ParseInt(rq.r.FormValue("mh"), 10, 64)
	rq.zoom, _ = strconv.ParseFloat(rq.r.FormValue("z"), 64)
	if rq.zoom < 0.1 {
		rq.zoom = 1.0
	}
	rq.colors, _ = strconv.ParseInt(rq.r.FormValue("c"), 10, 64)
	if rq.colors < 2 || rq.colors > 256 {
		rq.colors = defGeom.c
	}
	rq.keys = rq.r.FormValue("k")
	rq.buttons = rq.r.FormValue("Fn")
	rq.imgType = rq.r.FormValue("t")
	switch rq.imgType {
	case "png":
	case "gif":
	case "jpg":
	case "iff":
	default:
		rq.imgType = *defType
	}
	//log.Printf("%s WrpReq from UI Form: %+v\n", rq.r.RemoteAddr, rq)
}

// Display WP UI
func (rq *wrpReq) printHTML(p printParams) {
	rq.w.Header().Set("Cache-Control", "max-age=0")
	rq.w.Header().Set("Expires", "-1")
	rq.w.Header().Set("Pragma", "no-cache")
	rq.w.Header().Set("Content-Type", "text/html")
	// special hack for ' seems not to translate well to HTML, replace it with `
	rq.title = strings.Replace(rq.title, "'", "`", -1)
	data := uiData{
		Version:    version,
		URL:        rq.url,
		BgColor:    p.bgColor,
		Width:      rq.width,
		Height:     rq.height,
		NColors:    rq.colors,
		Zoom:       rq.zoom,
		ImgType:    rq.imgType,
		ImgSize:    p.imgSize,
		ImgWidth:   p.imgWidth,
		ImgHeight:  p.imgHeight,
		ImgURL:     p.imgURL,
		MapURL:     p.mapURL,
		PageHeight: p.pageHeight,
		Title:      html.EscapeString(rq.title),
		DownURL:    rq.downurl,
		DownType:   rq.downtype,
	}
	err := htmlTmpl.Execute(rq.w, data)
	if err != nil {
		log.Fatal(err)
	}
}

func isDownloadable(mime string) bool {
	notOK := [...]string{
		"application/javascript",
		"application/x-javascript",
		"application/xhtml+xml",
		"application/x-httpd-php",
		"application/xml",
		"application/json",
		"image/gif",
		"image/jpeg",
		"image/jpg",
		"image/png",
		"image/svg",
		"image/webp",
		"text/",
		"font/",
	}
	for i := 0; i < len(notOK); i++ {
		if strings.Contains(mime, notOK[i]) {
			return false
		}
	}
	return true
}

func MouseDrag(x1, y1, x2, y2 float64, opts ...chromedp.MouseOption) chromedp.MouseAction {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		p := &input.DispatchMouseEventParams{
			Type:       input.MousePressed,
			X:          x1,
			Y:          y1,
			Button:     input.Left,
			ClickCount: 1,
		}

		// apply opts
		for _, o := range opts {
			p = o(p)
		}

		if err := p.Do(ctx); err != nil {
			return err
		}

		p.Type = input.MouseMoved
		p.X = x2
		p.Y = y2

		if err := p.Do(ctx); err != nil {
			return err
		}

		p.Type = input.MouseReleased
		return p.Do(ctx)
	})
}

func MouseClick2(x, y float64, opts ...chromedp.MouseOption) chromedp.MouseAction {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		p := &input.DispatchMouseEventParams{
			Type:       input.MousePressed,
			X:          x,
			Y:          y,
			Button:     input.Left,
			ClickCount: 2,
		}

		// apply opts
		for _, o := range opts {
			p = o(p)
		}

		if err := p.Do(ctx); err != nil {
			return err
		}

		p.Type = input.MouseReleased
		return p.Do(ctx)
	})
}

// Determine what action to take
func (rq *wrpReq) action() chromedp.Action {
	rq.downurl = ""
	rq.downtype = ""
	var IsSet bool
	IsSet = false
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		switch ev := ev.(type) {
		case *network.EventResponseReceived:
			{
				if isDownloadable(ev.Response.MimeType) {
					if !IsSet {
						rq.downurl = ev.Response.URL
						rq.downtype = ev.Response.MimeType
						IsSet = true
					}
				}
			}
		}
	})
	// Mouse Click
	if rq.mouseX > 0 && rq.mouseY > 0 {
		if rq.mouseX == rq.mouseX2 && rq.mouseY == rq.mouseY2 {
			log.Printf("%s Mouse Click %d,%d\n", rq.r.RemoteAddr, rq.mouseX, rq.mouseY)
			return chromedp.MouseClickXY(float64(rq.mouseX)/float64(rq.zoom), float64(rq.mouseY)/float64(rq.zoom))
		} else {
			log.Printf("%s Mouse Move Click %d,%d,%d,%d\n", rq.r.RemoteAddr, rq.mouseX, rq.mouseY, rq.mouseX2, rq.mouseY2)
			return MouseDrag(float64(rq.mouseX)/float64(rq.zoom), float64(rq.mouseY)/float64(rq.zoom), float64(rq.mouseX2)/float64(rq.zoom), float64(rq.mouseY2)/float64(rq.zoom))
		}
	}

	// Buttons
	if len(rq.buttons) > 0 {
		log.Printf("%s Button %v\n", rq.r.RemoteAddr, rq.buttons)
		switch rq.buttons {
		case "Bk":
			return chromedp.NavigateBack()
		case "Fw":
			return chromedp.NavigateForward()
		case "St":
			return chromedp.Stop()
		case "Re":
			return chromedp.Reload()
		case "Bs":
			return chromedp.KeyEvent("\b")
		case "Rt":
			return chromedp.KeyEvent("\r")
		case "<":
			return chromedp.KeyEvent(kb.ArrowLeft)
		case "^":
			return chromedp.KeyEvent(kb.ArrowUp)
		case "v":
			return chromedp.KeyEvent(kb.ArrowDown)
		case ">":
			return chromedp.KeyEvent(kb.ArrowRight)
		case "Pu":
			return chromedp.KeyEvent(kb.PageUp)
		case "Pd":
			return chromedp.KeyEvent(kb.PageDown)
		}
	}
	// Keys
	if len(rq.keys) > 0 {
		return chromedp.KeyEvent(rq.keys)
	}
	// Navigate to URL
	log.Printf("%s Processing Capture Request for %s\n", rq.r.RemoteAddr, rq.url)
	return chromedp.Navigate(rq.url)
}

// Navigate to the desired URL.
func (rq *wrpReq) navigate() {
	ctxErr(chromedp.Run(ctx, rq.action()), rq.w)
}

// Handle context errors
func ctxErr(err error, w io.Writer) {
	// TODO: callers should have retry logic, perhaps create another function
	// that takes ...chromedp.Action and retries with give up
	if err == nil {
		return
	}
	log.Printf("Context error: %s", err)
	fmt.Fprintf(w, "Context error: %s<BR>\n", err)
	if err.Error() != "context canceled" {
		return
	}
	ctx, cncl = chromedp.NewContext(actx)
	log.Printf("Created new context, try again")
	fmt.Fprintln(w, "Created new context, try again")
}

// https://github.com/chromedp/chromedp/issues/979
func chromedpCaptureScreenshot(res *[]byte, h int64) chromedp.Action {
	if res == nil {
		panic("res cannot be nil")
	}
	if h == 0 {
		return chromedp.CaptureScreenshot(res)
	}

	return chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		*res, err = page.CaptureScreenshot().Do(ctx)
		return err
	})
}

func gifPalette(i image.Image, n int64) image.Image {
	switch n {
	case 2:
		i = halfgone.FloydSteinbergDitherer{}.Apply(halfgone.ImageToGray(i))
	case 216:
		var FastGifLut = [256]int{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5}
		r := i.Bounds()
		// NOTE: the color index computation below works only for palette.WebSafe!
		p := image.NewPaletted(r, palette.WebSafe)
		if i64, ok := i.(image.RGBA64Image); ok {
			for y := r.Min.Y; y < r.Max.Y; y++ {
				for x := r.Min.X; x < r.Max.X; x++ {
					c := i64.RGBA64At(x, y)
					r6 := FastGifLut[c.R>>8]
					g6 := FastGifLut[c.G>>8]
					b6 := FastGifLut[c.B>>8]
					p.SetColorIndex(x, y, uint8(36*r6+6*g6+b6))
				}
			}
		} else {
			for y := r.Min.Y; y < r.Max.Y; y++ {
				for x := r.Min.X; x < r.Max.X; x++ {
					c := i.At(x, y)
					r, g, b, _ := c.RGBA()
					r6 := FastGifLut[r&0xff]
					g6 := FastGifLut[g&0xff]
					b6 := FastGifLut[b&0xff]
					p.SetColorIndex(x, y, uint8(36*r6+6*g6+b6))
				}
			}
		}
		i = p
	default:
		q := median.Quantizer(n)
		i = q.Paletted(i)
	}
	return i
}

// Capture currently rendered web page to an image and fake ISMAP
func (rq *wrpReq) capture() {
	var styles []*css.ComputedStyleProperty
	var r, g, b int
	var h int64
	var pngCap []byte
	chromedp.Run(ctx,
		emulation.SetDeviceMetricsOverride(int64(float64(rq.width)/rq.zoom), 10, rq.zoom, false),
		chromedp.Location(&rq.url),
		chromedp.Title(&rq.title),
		chromedp.ComputedStyle("body", &styles, chromedp.ByQuery),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, _, _, _, _, s, err := page.GetLayoutMetrics().Do(ctx)
			if err == nil {
				h = int64(math.Ceil(s.Height))
			}
			return nil
		}),
	)
	for _, style := range styles {
		if style.Name == "background-color" {
			fmt.Sscanf(style.Value, "rgb(%d,%d,%d)", &r, &g, &b)
		}
	}
	log.Printf("%s Landed on: %s, Height: %v\n", rq.r.RemoteAddr, rq.url, h)
	height := int64(float64(rq.height) / rq.zoom)
	if rq.height == 0 && h > 0 {
		height = h + 30
		if height < int64(float64(rq.minheight)/rq.zoom) {
			height = int64(float64(rq.minheight) / rq.zoom)
		}
	}
	chromedp.Run(
		ctx, emulation.SetDeviceMetricsOverride(int64(float64(rq.width)/rq.zoom), height, rq.zoom, false),
		chromedp.Sleep(*delay), // TODO(tenox): find a better way to determine if page is rendered
	)
	// Capture screenshot...
	ctxErr(chromedp.Run(ctx, chromedpCaptureScreenshot(&pngCap, rq.height)), rq.w)
	seq := rand.Intn(9999)
	imgPath := fmt.Sprintf("/img/%04d.%s", seq, rq.imgType)
	mapPath := fmt.Sprintf("/map/%04d.map", seq)
	ismap[mapPath] = *rq
	var sSize string
	var iW, iH int
	switch rq.imgType {
	case "png":
		pngBuf := bytes.NewBuffer(pngCap)
		img[imgPath] = *pngBuf
		cfg, _, _ := image.DecodeConfig(pngBuf)
		sSize = fmt.Sprintf("%.0f KB", float32(len(pngBuf.Bytes()))/1024.0)
		iW = cfg.Width
		iH = cfg.Height

		if *imgOpti {
			img[imgPath] = *bytes.NewBuffer(optimizeImageFile(imgPath, img[imgPath]))
		}

		log.Printf("%s Got PNG image: %s, original size: %s, Res: %dx%d\n", rq.r.RemoteAddr, imgPath, sSize, iW, iH)
	case "iff":
		pngBuf := bytes.NewBuffer(pngCap)
		img[imgPath] = *pngBuf
		cfg, _, _ := image.DecodeConfig(pngBuf)
		sSize = fmt.Sprintf("%.0f KB", float32(len(pngBuf.Bytes()))/1024.0)
		iW = cfg.Width
		iH = cfg.Height
		log.Printf("%s Got PNG image: %s, Size: %s, Res: %dx%d\n", rq.r.RemoteAddr, imgPath, sSize, iW, iH)
		img[imgPath] = *bytes.NewBuffer(convertToIFF(imgPath, rq.colors, img[imgPath]))
	case "gif":
		i, err := png.Decode(bytes.NewReader(pngCap))
		if err != nil {
			log.Printf("%s Failed to decode PNG screenshot: %s\n", rq.r.RemoteAddr, err)
			fmt.Fprintf(rq.w, "<BR>Unable to decode page PNG screenshot:<BR>%s<BR>\n", err)
			return
		}
		st := time.Now()
		var gifBuf bytes.Buffer
		err = gif.Encode(&gifBuf, gifPalette(i, rq.colors), &gif.Options{})
		if err != nil {
			log.Printf("%s Failed to encode GIF: %s\n", rq.r.RemoteAddr, err)
			fmt.Fprintf(rq.w, "<BR>Unable to encode GIF:<BR>%s<BR>\n", err)
			return
		}
		img[imgPath] = gifBuf
		sSize = fmt.Sprintf("%.0f KB", float32(len(gifBuf.Bytes()))/1024.0)
		iW = i.Bounds().Max.X
		iH = i.Bounds().Max.Y
		log.Printf("%s Encoded GIF image: %s, Size: %s, Colors: %d, Res: %dx%d, Time: %vms\n", rq.r.RemoteAddr, imgPath, sSize, rq.colors, iW, iH, time.Since(st).Milliseconds())
	case "jpg":
		i, err := png.Decode(bytes.NewReader(pngCap))
		if err != nil {
			log.Printf("%s Failed to decode PNG screenshot: %s\n", rq.r.RemoteAddr, err)
			fmt.Fprintf(rq.w, "<BR>Unable to decode page PNG screenshot:<BR>%s<BR>\n", err)
			return
		}
		st := time.Now()
		var jpgBuf bytes.Buffer
		err = jpeg.Encode(&jpgBuf, i, &jpeg.Options{Quality: *jpgQual})
		if err != nil {
			log.Printf("%s Failed to encode JPG: %s\n", rq.r.RemoteAddr, err)
			fmt.Fprintf(rq.w, "<BR>Unable to encode JPG:<BR>%s<BR>\n", err)
			return
		}
		img[imgPath] = jpgBuf
		sSize = fmt.Sprintf("%.0f KB", float32(len(jpgBuf.Bytes()))/1024.0)
		iW = i.Bounds().Max.X
		iH = i.Bounds().Max.Y
		log.Printf("%s Encoded JPG image: %s, Size: %s, Quality: %d, Res: %dx%d, Time: %vms\n", rq.r.RemoteAddr, imgPath, sSize, *jpgQual, iW, iH, time.Since(st).Milliseconds())
	}

	rq.printHTML(printParams{
		bgColor:    fmt.Sprintf("#%02X%02X%02X", r, g, b),
		pageHeight: fmt.Sprintf("%d PX", h),
		imgSize:    sSize,
		imgURL:     imgPath,
		mapURL:     mapPath,
		imgWidth:   iW,
		imgHeight:  iH,
	})
	log.Printf("%s Done with capture for %s\n", rq.r.RemoteAddr, rq.url)
}

func checkBearerToken(r *http.Request) bool {
	if *token == "" {
		return true
	}

	requestToken := r.Header.Get("Bearer")
	if requestToken == *token {
		return true
	}

	log.Printf("%s Invalid token given in Bearer: '%s' [%+v]\n", r.RemoteAddr, requestToken, r.URL.RawQuery)
	return false
}

// Process HTTP requests to WRP '/' url
func pageServer(w http.ResponseWriter, r *http.Request) {
	if !checkBearerToken(r) {
		w.WriteHeader(http.StatusUnauthorized)
		w.(http.Flusher).Flush()

		return
	}

	log.Printf("%s Page Request for %s [%+v]\n", r.RemoteAddr, r.URL.Path, r.URL.RawQuery)
	rq := wrpReq{
		r: r,
		w: w,
	}
	rq.parseForm()
	if len(rq.url) < 4 {
		rq.printHTML(printParams{bgColor: "#FFFFFF"})
		return
	}
	rq.navigate() // TODO: if error from navigate do not capture
	rq.capture()
}

// Process HTTP requests to ISMAP '/map/' url
func mapServer(w http.ResponseWriter, r *http.Request) {
	if !checkBearerToken(r) {
		w.WriteHeader(http.StatusUnauthorized)
		w.(http.Flusher).Flush()

		return
	}

	log.Printf("%s ISMAP Request for %s [%+v]\n", r.RemoteAddr, r.URL.Path, r.URL.RawQuery)
	rq, ok := ismap[r.URL.Path]
	rq.r = r
	rq.w = w
	if !ok {
		fmt.Fprintf(w, "Unable to find map %s\n", r.URL.Path)
		log.Printf("Unable to find map %s\n", r.URL.Path)
		return
	}
	if !*noDel {
		defer delete(ismap, r.URL.Path)
	}
	n, err := fmt.Sscanf(r.URL.RawQuery, "%d,%d", &rq.mouseX, &rq.mouseY)
	if err != nil || n != 2 {
		fmt.Fprintf(w, "n=%d, err=%s\n", n, err)
		log.Printf("%s ISMAP n=%d, err=%s\n", r.RemoteAddr, n, err)
		return
	}
	rq.mouseX2 = rq.mouseX
	rq.mouseY2 = rq.mouseY
	n1, err1 := fmt.Sscanf(r.URL.RawQuery, "%d,%d,%d,%d", &rq.mouseX, &rq.mouseY, &rq.mouseX2, &rq.mouseY2)
	if err1 != nil || n1 != 4 {
		rq.mouseX2 = rq.mouseX
		rq.mouseY2 = rq.mouseY
	}
	//
	log.Printf("%s WrpReq from ISMAP: %+v\n", r.RemoteAddr, rq)
	if len(rq.url) < 4 {
		rq.printHTML(printParams{bgColor: "#FFFFFF"})
		return
	}
	rq.navigate() // TODO: if error from navigate do not capture
	rq.capture()
}

// Process HTTP requests for images '/img/' url
func imgServer(w http.ResponseWriter, r *http.Request) {
	if !checkBearerToken(r) {
		w.WriteHeader(http.StatusUnauthorized)
		w.(http.Flusher).Flush()

		return
	}

	log.Printf("%s IMG Request for %s\n", r.RemoteAddr, r.URL.Path)
	imgBuf, ok := img[r.URL.Path]
	if !ok || imgBuf.Bytes() == nil {
		fmt.Fprintf(w, "Unable to find image %s\n", r.URL.Path)
		log.Printf("%s Unable to find image %s\n", r.RemoteAddr, r.URL.Path)
		return
	}
	if !*noDel {
		defer delete(img, r.URL.Path)
	}
	switch {
	case strings.HasPrefix(r.URL.Path, ".gif"):
		w.Header().Set("Content-Type", "image/gif")
	case strings.HasPrefix(r.URL.Path, ".png"):
		w.Header().Set("Content-Type", "image/png")
	case strings.HasPrefix(r.URL.Path, ".jpg"):
		w.Header().Set("Content-Type", "image/jpeg")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(imgBuf.Bytes())))
	w.Header().Set("Cache-Control", "max-age=0")
	w.Header().Set("Expires", "-1")
	w.Header().Set("Pragma", "no-cache")
	w.Write(imgBuf.Bytes())
	w.(http.Flusher).Flush()
}

func clipServer(w http.ResponseWriter, r *http.Request) {
	if !checkBearerToken(r) {
		w.WriteHeader(http.StatusUnauthorized)
		w.(http.Flusher).Flush()

		return
	}
	log.Printf("%s Clip requested: %s\n", r.RemoteAddr, r.URL.RawQuery)
	clipboardPermission := browser.PermissionDescriptor{Name: "clipboard-read"}
	actions := make(chromedp.Tasks, 0)
	actions = append(actions, browser.SetPermission(&clipboardPermission, browser.PermissionSettingGranted))
	if r.URL.RawQuery == "copy" {
		actions = append(actions,
			chromedp.Evaluate(`window.getSelection().toString()`, &clipcontent, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
				return p.WithAwaitPromise(true)
			}))
	} else {
		actions = append(actions,
			chromedp.Evaluate(`window.navigator.clipboard.readText()`, &clipcontent, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
				return p.WithAwaitPromise(true)
			}))
	}
	chromedp.Run(ctx, actions)
	w.Header().Set("Content-Length", strconv.Itoa(len(clipcontent)))
	w.Header().Set("Cache-Control", "max-age=0")
	w.Header().Set("Expires", "-1")
	w.Header().Set("Pragma", "no-cache")
	w.Write(clipcontent)
	w.(http.Flusher).Flush()
}

// Process HTTP requests for Shutdown via '/shutdown/' url
func haltServer(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s Shutdown Request for %s\n", r.RemoteAddr, r.URL.Path)
	w.Header().Set("Cache-Control", "max-age=0")
	w.Header().Set("Expires", "-1")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "Shutting down WRP...\n")
	w.(http.Flusher).Flush()
	time.Sleep(time.Second * 2)
	cncl()
	acncl()
	srv.Shutdown(context.Background())
	os.Exit(1)
}

// returns html template, either from html file or built-in
func tmpl(t string) string {
	var tmpl []byte
	fh, err := os.Open(t)
	if err != nil {
		goto builtin
	}
	defer fh.Close()

	tmpl, err = ioutil.ReadAll(fh)
	if err != nil {
		goto builtin
	}
	log.Printf("Got HTML UI template from %v file, size %v \n", t, len(tmpl))
	return string(tmpl)

builtin:
	fhs, err := fs.Open("wrp.html")
	if err != nil {
		log.Fatal(err)
	}
	defer fhs.Close()

	tmpl, err = ioutil.ReadAll(fhs)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Got HTML UI template from embed\n")
	return string(tmpl)
}

// Print my own IP addresses
func printIPs(b string) {
	ap := strings.Split(b, ":")
	if len(ap) < 1 {
		log.Fatal("Wrong format of ipaddress:port")
	}
	log.Printf("Listen address: %v", b)
	if ap[0] != "" && ap[0] != "0.0.0.0" {
		return
	}
	a, err := net.InterfaceAddrs()
	if err != nil {
		log.Print("Unable to get interfaces: ", err)
		return
	}
	var m string
	for _, i := range a {
		n, ok := i.(*net.IPNet)
		if !ok || n.IP.IsLoopback() || strings.Contains(n.IP.String(), ":") {
			continue
		}
		m = m + n.IP.String() + " "
	}
	log.Print("My IP addresses: ", m)
}

func optimizeImageFile(imgPath string, buffer bytes.Buffer) []byte {
	var tmpFileName = strings.Replace(imgPath, "/img/", "/tmp/", 1)

	err := ioutil.WriteFile(tmpFileName, buffer.Bytes(), 0644)
	if err != nil {
		log.Print("Unable to write tempfile to optimize image: ", err)
	}

	_, err = exec.Command("optipng", tmpFileName).Output()
	if err != nil {
		log.Print("Unable to optimize PNG image: ", err)
	}

	fi, err := os.Stat(tmpFileName)
	if err != nil {
		log.Print("Unable to readback optimized image: ", err)
	}
	size := fi.Size() / 1024

	b, err := os.ReadFile(tmpFileName)
	if err != nil {
		log.Print("Unable to readback optimized image: ", err)
	}

	log.Printf("Optimized image, new filesize: %d KB", size)
	defer func(name string) {
		err := os.Remove(name)
		if err != nil {
			log.Printf("Can't unlink tmp file")
		}
	}(tmpFileName)

	return b
}

func convertToIFF(imgPath string, colors int64, buffer bytes.Buffer) []byte {
	var tmpFileName = strings.Replace(imgPath, ".iff", ".png", 1)
	tmpFileName = strings.Replace(tmpFileName, "/img/", "/tmp/", 1)
	var targetFileName = strings.Replace(imgPath, "/img/", "/tmp/", 1)
	var scriptName = strings.Replace(targetFileName, ".iff", ".sh", 1)

	err := ioutil.WriteFile(tmpFileName, buffer.Bytes(), 0644)
	if err != nil {
		log.Print("Unable to write tempfile to convert to IFF: ", err)
	}

	fi, err := os.Stat(tmpFileName)
	if err != nil {
		log.Print("Unable to readback IFF image: ", err)
	}
	var oldSize = fi.Size() / 1024

	var depth int64 = int64(math.Round(math.Log2(float64(colors))))

	var script = "#!/bin/bash\npngtopnm " + tmpFileName + " | ppmquant -nofs " + fmt.Sprint(colors) + " | ppmtoilbm -fixplanes " + fmt.Sprint(depth) + " >" + targetFileName
	{
		err := ioutil.WriteFile(scriptName, []byte(script), 0700)
		if err != nil {
			log.Print("Unable to write script to convert to IFF: ", err)
		}
	}
	f1, err1 := exec.Command(scriptName).Output()
	if err1 != nil {
		log.Print("Unable to convert to IFF: ", err1.Error(), " ", string(f1))
	}
	os.Remove(tmpFileName)
	os.Remove(scriptName)

	fi1, err := os.Stat(targetFileName)
	if err != nil {
		log.Print("Unable to readback IFF image: ", err)
	}
	size := fi1.Size() / 1024
	var nbuffer []byte
	if size > 0 {
		b, err := os.ReadFile(targetFileName)
		if err != nil {
			log.Print("Unable to readback IFF image: ", err)
			nbuffer = buffer.Bytes()
		} else {
			nbuffer = b
		}
	} else {
		nbuffer = buffer.Bytes()
	}
	log.Printf("converted image old size %d KB, new filesize: %d KB", oldSize, size)
	defer func(name string) {
		err := os.Remove(name)
		if err != nil {
			log.Printf("Can't unlink tmp file")
		}
	}(targetFileName)

	return nbuffer
}

// Main
func main() {
	var err error
	flag.Parse()

	if *logTarget != "" {
		logfile, err := os.OpenFile(*logTarget, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
		if err == nil {
			log.SetOutput(logfile)
		} else {
			log.Printf("Logfile '%s' could not be opened!", *logTarget)
		}
	}

	log.Printf("Web Rendering Proxy Version %s\n", version)
	log.Printf("Args: %q", os.Args)
	if *token != "" {
		log.Printf("Authentication configured: %s must be given as Bearer header in all requests", *token)
	} else {
		log.Printf("Notice: NO AUTHENTICATION CONFIGURED!")
	}
	if len(os.Getenv("PORT")) > 0 {
		*addr = ":" + os.Getenv(("PORT"))
	}
	printIPs(*addr)
	printIPs(*tlsAddr)
	n, err := fmt.Sscanf(*fgeom, "%dx%dx%d", &defGeom.w, &defGeom.h, &defGeom.c)
	if err != nil || n != 3 {
		log.Fatalf("Unable to parse -g geometry flag / %s", err)
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", *headless),
		chromedp.Flag("hide-scrollbars", false),
	)
	actx, acncl = chromedp.NewExecAllocator(context.Background(), opts...)
	defer acncl()

	ctx, cncl = chromedp.NewContext(actx)
	defer cncl()

	rand.Seed(time.Now().UnixNano())

	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		log.Printf("Interrupt - shutting down.")
		cncl()
		acncl()
		srv.Shutdown(context.Background())
		os.Exit(1)
	}()

	http.HandleFunc("/", pageServer)
	http.HandleFunc("/map/", mapServer)
	http.HandleFunc("/img/", imgServer)
	http.HandleFunc("/clip/", clipServer)
	http.HandleFunc("/shutdown/", haltServer)
	http.HandleFunc("/favicon.ico", http.NotFound)

	log.Printf("Default Img Type: %v, Geometry: %+v", *defType, defGeom)

	htmlTmpl, err = template.New("wrp.html").Parse(tmpl(*htmFnam))
	if err != nil {
		log.Fatal(err)
	}

	if _, err := os.Stat("./cert.crt"); err == nil {
		if _, err := os.Stat("./private.key"); err == nil {
			log.Print("Starting WRP https server")
			httpsSrv.Addr = *tlsAddr
			go func() {
				err = httpsSrv.ListenAndServeTLS("cert.crt", "private.key")
				if err != nil {
					log.Print("TLS server startup failed, only HTTP will be available. Reason: ", err)
				}
			}()
		} else {
			log.Print("No private key for the SSL certificate available, can't start https server.")
		}
	} else {
		log.Print("No SSL certificate available, can't start https server.")
	}

	log.Print("Starting WRP http server")
	srv.Addr = *addr
	err = srv.ListenAndServe()
	if err != nil {
		log.Fatal("http server err: ", err)
	}
}
