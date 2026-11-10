package agenttool

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

// OpenRemotePairQR presents a pairing QR in an operator-only browser pane. The
// QR exists only in memory, never in pane argv, the conversation or a file. A
// single-use local trampoline holds an unrelated random capability until the
// protected pane has loaded the page; it has no browser control endpoint.
func OpenRemotePairQR(ctx context.Context, cwd, pairURL string) (func(), error) {
	u, err := url.Parse(pairURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || !strings.HasPrefix(u.Fragment, "token=") {
		return nil, errors.New("invalid remote pairing URL")
	}
	png, err := qrcode.Encode(pairURL, qrcode.Medium, 480)
	if err != nil {
		return nil, errors.New("cannot render remote pairing QR")
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("cannot start private pairing viewer")
	}
	path := "/mcp-start/" + rand.Text()
	page := "<!doctype html><html><head><meta charset=utf-8><title>Golder private pairing</title><style>body{background:#101827;color:#eee;font:20px sans-serif;text-align:center;padding:24px}img{width:360px;max-width:80vw;background:white;padding:16px}</style></head><body><h2>Scan with your phone</h2><p>Only pair the Mac and session you recognize.</p><img alt='Golder pairing QR' src='data:image/png;base64," + base64.StdEncoding.EncodeToString(png) + "'></body></html>"
	var mu sync.Mutex
	served := false
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if served || r.Method != http.MethodGet || r.URL.Path != path || r.Host != ln.Addr().String() {
			http.NotFound(w, r)
			return
		}
		served = true
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src data:; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
		_, _ = w.Write([]byte(page))
		page = ""
	})}
	go func() { _ = server.Serve(ln) }()
	closePane, err := OpenAuthorizationPage(ctx, cwd, "http://"+ln.Addr().String()+path)
	if err != nil {
		_ = server.Close()
		return nil, err
	}
	var closeOnce sync.Once
	closeAll := func() {
		closeOnce.Do(func() {
			_ = server.Close()
			closePane()
		})
	}
	time.AfterFunc(2*time.Minute, closeAll)
	return closeAll, nil
}
