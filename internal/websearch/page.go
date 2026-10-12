package websearch

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// Page is the readable text of a web page.
type Page struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

// Reader opens web pages for the chat: the pages its search found or the user named. Only public
// addresses are reached — every connection, redirects included, is checked after DNS — so a page
// cannot steer the service to the router, the NAS or this PC.
type Reader struct {
	HTTP *http.Client
	// AllowPrivate lets tests read from a local server.
	AllowPrivate bool
}

const (
	maxPage      = 2 << 20 // bytes of HTML read from one page
	maxPageText  = 20000   // characters of text kept from one page
	maxRedirects = 3
)

var errPrivate = errors.New("адрес в локальной сети недоступен для чтения")

// blocked are ranges that are not the public internet beyond what netip reports itself.
var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("64:ff9b::/96"),
}

// Public reports whether an address is on the public internet.
func Public(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

func (r Reader) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	dialer := &net.Dialer{Timeout: 8 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		a, err := netip.ParseAddr(host)
		if err != nil || (!r.AllowPrivate && !Public(a)) {
			return errPrivate
		}
		return nil
	}}
	return &http.Client{
		Timeout: 15 * time.Second,
		// No proxy: the address checked above must be the one connected to.
		Transport: &http.Transport{Proxy: nil, DialContext: dialer.DialContext, TLSHandshakeTimeout: 8 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 64 << 10},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return errors.New("слишком много переадресаций")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("переадресация не на веб-страницу")
			}
			return nil
		},
	}
}

// CheckURL accepts an http(s) address of a site by name or public IP, without a password.
func CheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || len(raw) > 2000 {
		return nil, errors.New("нужен адрес веб-страницы http(s)")
	}
	if a, err := netip.ParseAddr(strings.Trim(u.Hostname(), "[]")); err == nil && !Public(a) {
		return nil, errPrivate
	}
	if h := strings.ToLower(u.Hostname()); h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".lan") || strings.HasSuffix(h, ".home.arpa") {
		return nil, errPrivate
	}
	u.Fragment = ""
	return u, nil
}

// Read fetches a page and returns its title and text.
func (r Reader) Read(ctx context.Context, raw string) (Page, error) {
	u, err := CheckURL(raw)
	if err != nil {
		return Page{}, fmt.Errorf("Страница не прочитана: %v.", err)
	}
	return r.read(ctx, u.String())
}

// read fetches a checked address; the dialer still refuses private addresses behind a name.
func (r Reader) read(ctx context.Context, raw string) (Page, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Page{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Page{}, err
	}
	// A browser's agent string, still naming OmniBox: weather and news sites refuse the bot-like
	// "compatible; …" one with 403 or a captcha, and the page the owner asked about is not read.
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 OmniBoxAI/1.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9")
	req.Header.Set("Accept-Language", "ru,en;q=0.8")
	resp, err := r.client().Do(req)
	if err != nil {
		if errors.Is(err, errPrivate) {
			return Page{}, fmt.Errorf("Страница не прочитана: %v.", errPrivate)
		}
		return Page{}, fmt.Errorf("Страница %s не открылась.", Host(u.String()))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Page{}, fmt.Errorf("Страница %s ответила %d.", Host(u.String()), resp.StatusCode)
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	plain := strings.HasPrefix(ct, "text/plain")
	if !plain && ct != "" && !strings.HasPrefix(ct, "text/html") && !strings.HasPrefix(ct, "application/xhtml") {
		return Page{}, fmt.Errorf("По адресу %s не веб-страница.", Host(u.String()))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPage))
	if err != nil && len(body) == 0 {
		return Page{}, fmt.Errorf("Страница %s не дочитана.", Host(u.String()))
	}
	text := decode(body, ct)
	p := Page{URL: resp.Request.URL.String()}
	if plain {
		p.Text = ClipText(tidy(text), maxPageText)
	} else {
		p.Title, p.Text = HTMLText(text)
		p.Text = ClipText(p.Text, maxPageText)
	}
	if strings.TrimSpace(p.Text) == "" {
		return Page{}, fmt.Errorf("На странице %s нет текста (возможно, она собирается скриптами).", Host(u.String()))
	}
	return p, nil
}

var (
	reTitle   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	reDrop    []*regexp.Regexp
	reMain    = regexp.MustCompile(`(?is)<(article|main)\b[^>]*>(.*?)</(?:article|main)>`)
	reBlock   = regexp.MustCompile(`(?i)<(?:br|/?p|/?div|/?li|/?h[1-6]|/?tr|/?section|/?blockquote|/?pre|/?table|/?ul|/?ol|/?dd|/?dt)\b[^>]*>`)
	reCell    = regexp.MustCompile(`(?i)</t[dh]>`)
	reTag     = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpaces  = regexp.MustCompile(`[ \t\f\v\x{00a0}]+`)
	reCharset = regexp.MustCompile(`(?i)charset\s*=\s*["']?([\w-]+)`)
)

func init() {
	// Go's regexp has no back-references: one pattern per element whose content is not text.
	for _, tag := range []string{"script", "style", "noscript", "svg", "template", "iframe", "head", "nav", "footer", "header", "aside", "form", "button", "select"} {
		reDrop = append(reDrop, regexp.MustCompile(`(?is)<`+tag+`\b[^>]*>.*?</`+tag+`\s*>`))
	}
}

// HTMLText is the title and the readable text of an HTML page: scripts, menus and footers are
// dropped, and the article or main part is preferred when the page marks one.
func HTMLText(doc string) (title, text string) {
	if m := reTitle.FindStringSubmatch(doc); m != nil {
		title = clip(tidy(html.UnescapeString(reTag.ReplaceAllString(m[1], ""))), 300)
	}
	doc = reComment.ReplaceAllString(doc, "")
	for _, re := range reDrop {
		doc = re.ReplaceAllString(doc, " ")
	}
	// The longest article or main element, when it holds the bulk of the text.
	best := ""
	for _, m := range reMain.FindAllStringSubmatch(doc, -1) {
		if len(m[2]) > len(best) {
			best = m[2]
		}
	}
	if len(best) > 500 {
		doc = best
	}
	doc = reBlock.ReplaceAllString(doc, "\n")
	doc = reCell.ReplaceAllString(doc, " \t")
	doc = reTag.ReplaceAllString(doc, "")
	return title, tidy(html.UnescapeString(doc))
}

// ClipText cuts text to n characters, keeping its lines.
func ClipText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:n])) + "…"
}

// tidy collapses spaces and drops empty lines.
func tidy(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(reSpaces.ReplaceAllString(line, " "))
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// decode turns the page into UTF-8: Russian sites still serve windows-1251 now and then.
func decode(body []byte, contentType string) string {
	charset := ""
	if m := reCharset.FindStringSubmatch(contentType); m != nil {
		charset = m[1]
	} else if m := reCharset.FindSubmatch(body[:min(len(body), 4096)]); m != nil {
		charset = string(m[1])
	}
	switch strings.ToLower(charset) {
	case "windows-1251", "cp1251", "win-1251":
		var b strings.Builder
		for _, c := range body {
			if c < 0x80 {
				b.WriteByte(c)
			} else {
				b.WriteRune(cp1251[c-0x80])
			}
		}
		return b.String()
	}
	if !utf8.Valid(body) {
		return strings.ToValidUTF8(string(body), "")
	}
	return string(body)
}

// cp1251 maps the upper half of windows-1251 to Unicode.
var cp1251 = []rune("ЂЃ‚ѓ„…†‡€‰Љ‹ЊЌЋЏђ‘’“”•–—�™љ›њќћџ ЎўЈ¤Ґ¦§Ё©Є«¬­®Ї°±Ііґµ¶·ё№є»јЅѕїАБВГДЕЖЗИЙКЛМНОПРСТУФХЦЧШЩЪЫЬЭЮЯабвгдежзийклмнопрстуфхцчшщъыьэюя")
