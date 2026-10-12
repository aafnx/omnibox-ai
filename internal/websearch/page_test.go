package websearch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestPublicAddresses(t *testing.T) {
	for addr, public := range map[string]bool{"8.8.8.8": true, "77.88.55.242": true, "2a00:1450:4010::64": true,
		"127.0.0.1": false, "10.0.0.5": false, "192.168.1.77": false, "172.16.3.4": false, "100.64.1.1": false, "169.254.1.1": false,
		"0.0.0.0": false, "::1": false, "fe80::1": false, "fd00::1": false, "::ffff:192.168.1.1": false, "224.0.0.1": false} {
		if got := Public(netip.MustParseAddr(addr)); got != public {
			t.Errorf("%s: %v", addr, got)
		}
	}
	for _, raw := range []string{"http://localhost/", "http://nas.local/x", "http://router.lan", "http://192.168.1.1/admin", "http://[::1]:8800/", "ftp://example.com/", "https://user:pw@example.com/", "javascript:alert(1)"} {
		if _, err := CheckURL(raw); err == nil {
			t.Errorf("%s accepted", raw)
		}
	}
	if u, err := CheckURL("https://yandex.ru/pogoda/kazan#now"); err != nil || u.String() != "https://yandex.ru/pogoda/kazan" {
		t.Errorf("%v %v", u, err)
	}
}

func TestHTMLText(t *testing.T) {
	title, text := HTMLText(`<html><head><title>Погода &amp; ветер</title><script>var x="<p>нет</p>";</script></head><body>
		<nav><a href="/">Меню</a></nav><article><h1>Казань</h1><p>Сегодня +15&nbsp;°C, ясно.</p><table><tr><td>Ветер</td><td>3 м/с</td></tr></table>` + strings.Repeat("<p>текст</p>", 80) + `</article><footer>Подвал</footer></body></html>`)
	if title != "Погода & ветер" {
		t.Errorf("title %q", title)
	}
	if !strings.Contains(text, "Казань\nСегодня +15 °C, ясно.") || !strings.Contains(text, "Ветер 3 м/с") || strings.Contains(text, "нет") || strings.Contains(text, "Меню") || strings.Contains(text, "Подвал") {
		t.Errorf("text %q", text[:min(len(text), 300)])
	}
}

func TestWindows1251(t *testing.T) {
	if len(cp1251) != 128 {
		t.Fatalf("table has %d runes", len(cp1251))
	}
	if got := decode([]byte("\xcf\xf0\xe8\xe2\xe5\xf2, \xa8\xeb\xea\xe0 \xb9"), "text/html; charset=windows-1251"); got != "Привет, Ёлка №" {
		t.Fatalf("%q", got)
	}
	if got := decode([]byte(`<meta charset="windows-1251"><p>`+"\xc4\xe0"), ""); !strings.HasSuffix(got, "Да") {
		t.Fatalf("meta charset: %q", got)
	}
}

func TestReadPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, `<title>Прогноз</title><main><p>Завтра дождь.</p>`+strings.Repeat("<p>подробности</p>", 60)+`</main>`)
		case "/moved":
			http.Redirect(w, r, "/page", http.StatusFound)
		case "/file":
			w.Header().Set("Content-Type", "application/zip")
			io.WriteString(w, "PK")
		case "/away":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		}
	}))
	defer srv.Close()
	// The test server is on this computer: only a reader allowed to reach it gets there.
	r := Reader{AllowPrivate: true}
	if _, err := (Reader{}).client().Get(srv.URL + "/page"); err == nil || !errors.Is(err, errPrivate) {
		t.Fatalf("a local address was reached: %v", err)
	}
	p, err := r.Read(context.Background(), strings.Replace(srv.URL, "127.0.0.1", "localhost.test", 1)+"/page")
	if err == nil {
		t.Fatalf("an unresolvable name read: %+v", p)
	}
	page, err := r.client().Get(srv.URL + "/moved")
	if err != nil || page.Request.URL.Path != "/page" {
		t.Fatalf("redirect: %v", err)
	}
	if _, err := r.client().Get(srv.URL + "/away"); err == nil {
		t.Fatal("a redirect off the web was followed")
	}
	got, err := r.read(context.Background(), srv.URL+"/moved")
	if err != nil || got.Title != "Прогноз" || !strings.HasPrefix(got.Text, "Завтра дождь.") || !strings.HasSuffix(got.URL, "/page") {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := r.read(context.Background(), srv.URL+"/file"); err == nil || !strings.Contains(err.Error(), "не веб-страница") {
		t.Fatalf("a zip read: %v", err)
	}
}
