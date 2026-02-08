package gopro

import (
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

var (
	baseCookieURL = urlMustParse("https://gopro.com")
)

func urlMustParse(rawURL string) *url.URL {
	u, err := url.Parse(rawURL)
	if err != nil {
		log.Panicf("Could not parse url %v", rawURL)
	}
	return u
}

func getCookieString(jar http.CookieJar, url *url.URL) string {
	cookieParts := []string{}
	// we only care about name and value.
	for _, cookie := range jar.Cookies(url) {
		cookieParts = append(cookieParts, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(cookieParts, ";")
}

// hookCookieJar extends cookiejar.Jar but calls a hook whenever cookies are updated
type hookCookieJar struct {
	*cookiejar.Jar
	hook func(jar http.CookieJar)
}

func newHookCookieJar(initialCookieString string, hook func(jar http.CookieJar)) (*hookCookieJar, error) {
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, err
	}

	if initialCookieString != "" {
		cookies, err := http.ParseCookie(initialCookieString)
		for _, cookie := range cookies {
			cookie.Domain = ".gopro.com"
			cookie.Expires = time.Now().Add(time.Hour * 24 * 7)
			cookie.Path = "/"
		}
		if err != nil {
			return nil, err
		}
		jar.SetCookies(baseCookieURL, cookies)
	}
	return &hookCookieJar{
		Jar:  jar,
		hook: hook,
	}, nil
}

// SetCookies handles the http.Set-Cookie headers in a response.
func (j *hookCookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.Jar.SetCookies(u, cookies)
	if j.hook != nil {
		j.hook(j.Jar)
	}
}
