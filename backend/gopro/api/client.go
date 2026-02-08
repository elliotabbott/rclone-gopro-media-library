// Package api provides functionality for interacting with the iCloud API.
package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/fshttp"
	"github.com/rclone/rclone/lib/rest"
)

var (
	baseCookieURL = urlMustParse("https://gopro.com")
	// default mediaFields to fetch from the API when loading media
	mediaFields = []string{
		strings.Join(
			[]string{
				"captured_at",
				"content_title",
				"created_at",
				"file_extension",
				"file_size",
				"filename",
				"id",
				"item_count",
				"ready_to_view",
				"type",
			},
			",",
		),
	}
)

// Client defines the client configuration
type Client struct {
	email    string
	password string
	srv      *rest.Client
	Session  *Session
}

// New creates a new gopro API Client
func New(email, password string, jar http.CookieJar) *Client {
	httpClient := fshttp.NewClient(context.Background())
	httpClient.Jar = jar

	srv := rest.NewClient(httpClient)
	for k, v := range defaultHeaders {
		srv.SetHeader(k, v)
	}

	c := &Client{
		email:    email,
		password: password,
		srv:      srv,
		Session:  NewSession(jar),
	}

	srv.SetErrorHandler(c.errorHandler)
	return c
}

// errorHandler tries to re-authenticate if there is an unauthorized status code
func (c *Client) errorHandler(resp *http.Response) error {
	// Copied impl rclone lib/rest/rest.go, not publicly exposed
	defaultErrorHandler := func() (err error) {
		body, err := rest.ReadBody(resp)
		if err != nil {
			return fmt.Errorf("error reading error out of body: %w", err)
		}
		return fmt.Errorf("HTTP error %v (%v) returned body: %q", resp.StatusCode, resp.Status, body)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return c.EnsureAuthenticated(resp.Request.Context())
	}
	return defaultErrorHandler()
}

func urlMustParse(rawURL string) *url.URL {
	u, err := url.Parse(rawURL)
	if err != nil {
		log.Panicf("Could not parse url %v", rawURL)
	}
	return u
}

func (c *Client) EnsureAuthenticated(ctx context.Context) error {
	err := c.Session.validateSession(ctx)
	if err == nil {
		// already authenticated or successfully refreshed
		fs.Infoc(c, "verified previous auth, no need to re authenticate")
		return nil
	}

	if err == ErrorMustAuthenticate {
		// get a fresh auth
		fs.Infoc(c, "validation of previous cookie failed, will reauthenticate")
		// TODO: figure out how to clear cookies first, I think this is broken
		c.Session.jar.SetCookies(baseCookieURL, nil)
		return c.Session.authenticate(ctx, c.email, c.password)
	}

	// some other error
	return err
}

func (c *Client) GetDownload(ctx context.Context, id string) (*goProMedia, error) {
	resJson := &goProMedia{}
	_, err := c.srv.CallJSON(
		ctx,
		&rest.Opts{
			RootURL: fmt.Sprintf("https://api.gopro.com/media/%v/download", id),
		},
		nil,
		resJson,
	)
	return resJson, err
}

type SearchOptions struct {
	CapturedRange string
	PageSize      int
	FilterTypes   string
}

func (c *Client) SearchMedia(ctx context.Context, opts SearchOptions) (*searchResult, error) {
	fs.Infof(c, "Starting to search media")
	// Iterate over the pages and return

	var res *searchResult
	pageNum := 1
	totalPages := 0
	totalItems := 0
	for {
		fs.Infof(c, "Getting page %v of %v (%v items)", pageNum, totalPages, totalItems)
		page, err := c.getPage(ctx, pageNum, opts)
		if err != nil {
			return nil, err
		}

		// first page
		if res == nil {
			res = page
			totalPages = *page.Pages.TotalPages
			totalItems = *page.Pages.TotalItems
		} else {
			res.Embedded.Media = append(res.Embedded.Media, page.Embedded.Media...)
		}

		// last page
		if pageNum >= totalPages {
			break
		}
		pageNum++
	}

	return res, nil
}

// getPage retrieves a single page of search results with pageNumber using 1-based indexing
func (c *Client) getPage(ctx context.Context, pageNumber int, opts SearchOptions) (*searchResult, error) {
	resJson := &searchResult{}

	params := url.Values{
		"per_page":       {strconv.Itoa(opts.PageSize)},
		"page":           {strconv.Itoa(pageNumber)}, // 1 based indexing
		"fields":         mediaFields,
		"captured_range": {opts.CapturedRange},
	}
	if opts.FilterTypes != "" {
		// The API expects comma separated values, not multiple query params
		params.Set("type", opts.FilterTypes)
	}

	_, err := c.srv.CallJSON(
		ctx,
		&rest.Opts{
			RootURL:    "https://api.gopro.com/media/search",
			Parameters: params,
		},
		nil,
		resJson,
	)
	return resJson, err
}

// GetMedia returns the media for metadata for a single file
func (c *Client) GetMedia(ctx context.Context, id string) (*Media, error) {
	resJson := &Media{}
	_, err := c.srv.CallJSON(
		ctx,
		&rest.Opts{
			RootURL: fmt.Sprintf("https://api.gopro.com/media/%v", id),
		},
		nil,
		resJson,
	)
	return resJson, err

}

func (c *Client) String() string {
	return fmt.Sprintf("GoProClient<%v>", c.email)
}

// topLevelMedia represents the entire JSON payload.
type goProMedia struct {
	Filename string        `json:"filename"`
	Embedded mediaEmbedded `json:"_embedded"`
}

// mediaEmbedded holds the nested arrays for files, variations, and sprites.
type mediaEmbedded struct {
	Files        []file        `json:"files"`
	Variations   []Variation   `json:"variations"` // where the videos are
	Sprites      []sprite      `json:"sprites"`
	SidecarFiles []sidecarFile `json:"sidecar_files"`
}

// file represents an individual file entry.
type file struct {
	URL            string `json:"url"`
	Head           string `json:"head"`
	CameraPosition string `json:"camera_position,omitempty"`
	ItemNumber     int    `json:"item_number,omitempty"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	Orientation    int    `json:"orientation,omitempty"`
	Available      bool   `json:"available"`
}

// Variation represents an individual video Variation.
type Variation struct {
	URL        string `json:"url"`
	Head       string `json:"head"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	ItemNumber *int   `json:"item_number,omitempty"` // Can be null
	Label      string `json:"label"`
	Type       string `json:"type"`
	Quality    string `json:"quality"`
	Available  bool   `json:"available"`
}

// sprite represents an individual sprite entry.
type sprite struct {
	Width      int      `json:"width"`
	Height     int      `json:"height"`
	Type       string   `json:"type"`
	FPS        float64  `json:"fps"`
	TotalCount int      `json:"total_count"`
	URLs       []string `json:"urls"`
	Heads      []string `json:"heads"`
	Frame      frame    `json:"frame"`
}

// frame for the frame details within a sprite.
type frame struct {
	Width  int `json:"width"`
	Height int `json:"height"`
	Count  int `json:"count"`
}

// sidecarFile represents an individual sidecar file entry.
type sidecarFile struct {
	URL   string  `json:"url"`
	Head  string  `json:"head"`
	Label string  `json:"label"`
	Type  string  `json:"type"`
	FPS   float64 `json:"fps"`
}
