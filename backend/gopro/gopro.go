// Package gopro implements the GoPro Media Library backend
package gopro

import (
	"context"

	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/fs/config/obscure"
	"github.com/rclone/rclone/fs/fshttp"
	"golang.org/x/sync/errgroup"

	"github.com/elliotabbott/rclone-gopro-media-library/backend/gopro/api"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/lib/encoder"
	"github.com/rclone/rclone/lib/pacer"
	"github.com/rclone/rclone/lib/rest"
)

const (
	configEmail    = "email"
	configPassword = "password"
	configCookies  = "cookies"
	configPageSize = "page_size"

	minSleep      = 10 * time.Millisecond
	maxSleep      = 2 * time.Second
	decayConstant = 2
)

// Register with Fs
func init() {
	fs.Register(&fs.RegInfo{
		Name:        "gopro",
		Description: "GoPro Media Library",
		Config:      Config,
		NewFs:       NewFs,
		Options: []fs.Option{{
			Name:      configEmail,
			Help:      "Email address",
			Required:  true,
			Sensitive: true,
		}, {
			Name:       configPassword,
			Help:       "Password.",
			Required:   true,
			IsPassword: true,
			Sensitive:  true,
		}, {
			Name:      configCookies,
			Help:      "cookies (internal use only)",
			Required:  false,
			Advanced:  false,
			Sensitive: true,
			Hide:      fs.OptionHideBoth,
		}, {
			Name:     configPageSize,
			Help:     "Number of items to return per page in a listing. Default is 100.",
			Required: false,
			Advanced: true,
			Default:  "100",
		}, {
			Name: "filter_types",
			Help: "Types to filter in GoPro Search API's type field. Comma separated list.",
			Examples: []fs.OptionExample{
				{Value: "MultiClipEdit"},
				{Value: "Photo"},
				{Value: "LoopedVideo"},
				{Value: "Video"},
				{Value: "TimeLapseVideo"},
			},
		}, {
			Name:     "captured_range",
			Help:     "Date range for searching media (e.g., '2024-03-01,2024-03-31').",
			Required: false,
			Advanced: true,
		}, {
			Name:     "head_requests",
			Help:     "Whether the to fetch file size metadata from GoPro using HEAD requests. This requires making API calls for each found media. Will use --checkers concurrent workers.",
			Required: false,
			Advanced: true,
			Default:  false,
		}, {
			Name:     config.ConfigEncoding,
			Help:     config.ConfigEncodingHelp,
			Advanced: true,
			Default: (encoder.Display |
				//encoder.EncodeDot |
				encoder.EncodeBackSlash |
				encoder.EncodeInvalidUtf8),
		}},
	})
}

// Options defines the configuration for this backend
type Options struct {
	Email         string               `config:"email"`
	Password      string               `config:"password"`
	Cookies       string               `config:"cookies"`
	PageSize      int                  `config:"page_size"`
	FilterTypes   string               `config:"filter_types"`
	Enc           encoder.MultiEncoder `config:"encoding"`
	CapturedRange string               `config:"captured_range"`
	HeadRequests  bool                 `config:"head_requests"`
}

// Fs represents a remote icloud drive
type Fs struct {
	name       string       // name of this remote
	root       string       // the path we are working on.
	opt        Options      // parsed config options
	features   *fs.Features // optional features
	gopro      *api.Client
	httpClient *http.Client
	pacer      *fs.Pacer      // pacer for API calls
	ci         *fs.ConfigInfo // global config
}

// Object describes an icloud drive object
type Object struct {
	fs *Fs // what this object is part of
	// The item index of the mediaId that this object represents. 1-based indexing. If zero,
	// this object only has one item
	itemIdx     int
	mediaId     string    // The GoPro media ID
	remote      string    // The remote path (relative to the fs.root)
	size        int64     // size of the object (on server, after encryption)
	modTime     time.Time // modification time of the object
	createdTime time.Time // creation time of the object
	contentType string
}

// Config configures the GoPro remote.
func Config(ctx context.Context, name string, m configmap.Mapper, _ fs.ConfigIn) (*fs.ConfigOut, error) {
	var err error
	email, _ := m.Get(configEmail)
	if email == "" {
		return nil, errors.New("an email address is required")
	}

	password, _ := m.Get(configPassword)
	if password != "" {
		password, err = obscure.Reveal(password)
		if err != nil {
			return nil, err
		}
	}

	cookieRaw, _ := m.Get(configCookies)
	// TODO: refactor this to just provide m and let it do everything else
	jar, err := newHookCookieJar(cookieRaw, makeConfigUpdateHook(m))
	if err != nil {
		return nil, err
	}

	gopro := api.New(email, password, jar)
	return nil, gopro.EnsureAuthenticated(ctx)
}

func makeConfigUpdateHook(m configmap.Mapper) func(http.CookieJar) {
	return func(jar http.CookieJar) {
		fs.Debugf("gopro-cookiejar", "updating cookiejar: %v", jar)
		m.Set(configCookies, getCookieString(jar, baseCookieURL))
	}
}

// NewFs constructs an Fs from the path, container:path
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	// Parse config into Options struct
	opt := new(Options)
	err := configstruct.Set(m, opt)
	if err != nil {
		return nil, err
	}

	if opt.Password != "" {
		var err error
		opt.Password, err = obscure.Reveal(opt.Password)
		if err != nil {
			return nil, fmt.Errorf("couldn't decrypt user password: %w", err)
		}
	}

	jar, err := newHookCookieJar(opt.Cookies, makeConfigUpdateHook(m))
	if err != nil {
		return nil, err
	}

	gopro := api.New(opt.Email, opt.Password, jar)
	// log in if needed
	if err := gopro.EnsureAuthenticated(ctx); err != nil {
		return nil, err
	}

	root = strings.Trim(root, "/")

	// Doesn't need cookies, just for downloading raw files
	httpClient := fshttp.NewClient(ctx)
	f := &Fs{
		name:       name,
		root:       root,
		gopro:      gopro,
		opt:        *opt,
		pacer:      fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(minSleep), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
		httpClient: httpClient,
		ci:         fs.GetConfig(ctx),
	}
	f.features = (&fs.Features{
		CanHaveEmptyDirectories: false,
		PartialUploads:          false,
	}).Fill(ctx, f)

	return f, nil
}

var (
	errorReadOnly = errors.New("http remotes are read only")
	timeUnset     = time.Unix(0, 0)
)

// statusError returns an error if the res contained an error
func statusError(res *http.Response, err error) error {
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		_ = res.Body.Close()
		return fmt.Errorf("HTTP Error: %s", res.Status)
	}
	return nil
}

// Name returns the configured name of the file system
func (f *Fs) Name() string {
	return f.name
}

// Root returns the root for the filesystem
func (f *Fs) Root() string {
	return f.root
}

// String returns the URL for the filesystem
func (f *Fs) String() string {
	return "notsuregopro"
}

// Features returns the optional features of this Fs
func (f *Fs) Features() *fs.Features {
	return f.features
}

// Precision is the remote http file system's modtime precision, which we have no way of knowing. We estimate at 1s
func (f *Fs) Precision() time.Duration {
	return time.Second
}

// NewObject creates a new remote http file object
func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	// Attempt to treat remote as a media id
	mediaId, err := parseMediaId(remote)
	if err != nil {
		return nil, err
	}
	media, err := f.gopro.GetMedia(ctx, mediaId)
	if err != nil {
		return nil, err
	}

	entries, err := f.objectsForMediaEntries(media)
	if err != nil {
		return nil, err
	}

	if len(entries) > 1 {
		return nil, fs.ErrorIsDir
	}

	o, ok := entries[0].(*Object)
	if !ok {
		return nil, fs.ErrorIsDir
	}

	return o, nil
}

// List the objects and directories in dir into entries.  The
// entries can be returned in any order but should be for a
// complete directory.
//
// dir should be "" to list the root, and should not have
// trailing slashes.
//
// This should return ErrDirNotFound if the directory isn't
// found.
func (f *Fs) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	// Because a single media file can have multiple parts (1.mp4, 2.mp4, etc.), we treat each
	// media as potentially multiple Objects.

	var media []api.Media
	if f.root != "" {
		// since everything is flat, this will refer to one specific media ID
		mediaId, err := parseMediaId(f.root)
		if err != nil {
			return nil, err
		}
		m, err := f.gopro.GetMedia(ctx, mediaId)
		if err != nil {
			return nil, err
		}
		media = append(media, *m)
	} else {
		// otherwise search the entire GoPro account
		searchResult, err := f.gopro.SearchMedia(
			ctx,
			api.SearchOptions{
				CapturedRange: f.opt.CapturedRange,
				PageSize:      f.opt.PageSize,
				FilterTypes:   f.opt.FilterTypes,
			},
		)
		if err != nil {
			return nil, err
		}
		media = searchResult.Embedded.Media
	}

	var entries fs.DirEntries
	for _, media := range media {
		// TODO: Skip jpg which are not plain "Photo" (type TimeLapse), which need different
		// logic for reading the download link from the API. There are two kinds of TimeLapse
		// videos as far as I can tell based on the play_as field: multi_shot_photo (which have
		// zip or individual photo downloads) and video (which have zip, individual photo, or
		// mp4 download). These break some of the invariants in this code (like item_count) and
		// would neeed some refactoring.
		if media.FileExtension == nil || (*media.FileExtension == "jpg" && *media.Type != "Photo") {
			continue
		}

		// Skip files that are not "ready" which won't have a download link. I see some with
		// "transcoding"
		if media.ReadyToView == nil || *media.ReadyToView != "ready" {
			continue
		}

		subentries, err := f.objectsForMediaEntries(&media)
		if err != nil {
			return nil, err
		}
		entries = append(entries, subentries...)
	}

	// Resolve metadata eagerly if configured to do so. This requires making a lot of requests
	// so it's off by default. Without it, rclone can't check for changes to file size.
	if f.opt.HeadRequests {
		if err := f.fetchMetadataParallel(ctx, entries); err != nil {
			return nil, err
		}
	}

	return entries, nil
}

func (f *Fs) fetchMetadataParallel(ctx context.Context, entries fs.DirEntries) error {
	g, errctx := errgroup.WithContext(ctx)
	g.SetLimit(f.ci.Checkers)

	fs.Infof(f, "fetching metadata in parallel with --checkers as limit=%v", f.ci.Checkers)
	for _, e := range entries {
		o, ok := e.(*Object)
		if !ok {
			continue
		}

		g.Go(func() error {
			return o.fetchMetadata(errctx)
		})
	}
	return g.Wait()
}

// objectsForMediaEntries converts a Media entry one or more Objects
func (f *Fs) objectsForMediaEntries(media *api.Media) (fs.DirEntries, error) {
	if media.ID == nil {
		return nil, fmt.Errorf("media ID is nil")
	}
	mediaId := *media.ID
	if mediaId == "" {
		return nil, fmt.Errorf("media ID is empty")
	}
	if media.ItemCount == nil {
		return nil, fmt.Errorf("media item count is nil")
	}
	itemCount := *media.ItemCount

	var entries fs.DirEntries
	for itemIdx := 1; itemIdx <= itemCount; itemIdx++ {
		o := &Object{
			fs:      f,
			remote:  mediaBasename(media, itemIdx),
			mediaId: mediaId,
			itemIdx: itemIdx,
			size:    -1,
			modTime: *media.CreatedAt,
		}
		entries = append(entries, o)
	}
	return entries, nil
}

func mediaBasename(media *api.Media, itemIdx int) string {
	// yyyymmdd_hhmmss format
	const dateFormat = "20060102_150405"
	ext := actualFileExtension(media)

	if media.Filename == nil || *media.Filename == "" {
		// $capturedat_$id_$itemIdx.mp4
		return fmt.Sprintf("%s_%s_%d.%s", media.CapturedAt.Format(dateFormat), *media.ID, itemIdx, ext)
	}

	// strip off any file extension from the filename since we add it at the end
	originalFilename := strings.Split(*media.Filename, ".")[0]

	// $capturedat_$filename_$id_$itemIdx.mp4
	return fmt.Sprintf("%s_%s_%s_%d.%s", media.CapturedAt.Format(dateFormat), originalFilename, *media.ID, itemIdx, ext)
}

func actualFileExtension(media *api.Media) string {
	if media.FileExtension == nil {
		// I don't think this will ever happen
		return "unknown"
	}

	switch *media.FileExtension {
	case "json":
		// MultiClipEdits return file_extension of JSON, but the "baked_source" download variation is the actual mp4 video
		return "mp4"
	default:
		return *media.FileExtension
	}
}

// parseMediaId returns the GoPro media ID from a string created by mediaBasename
func parseMediaId(remote string) (string, error) {
	parts := strings.Split(remote, "_")
	if len(parts) < 2 {
		// assume it's a raw media ID in this case and continue
		return remote, nil
	}
	// The media ID is the second to last part of the filename
	return parts[len(parts)-2], nil

}

// Put in to the remote path with the modTime given of the given size
//
// May create the object even if it returns an error - if so
// will return the object and the error, otherwise will return
// nil and the error
func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	return nil, errorReadOnly
}

// // PutStream uploads to the remote path with the modTime given of indeterminate size
// func (f *Fs) PutStream(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
// 	return nil, errorReadOnly
// }

// Fs is the filesystem this remote http file object is located within
func (o *Object) Fs() fs.Info {
	return o.fs
}

// String returns the URL to the remote HTTP file
func (o *Object) String() string {
	if o == nil {
		return "<nil>"
	}
	return o.remote
}

// Remote the name of the remote HTTP file, relative to the fs root
func (o *Object) Remote() string {
	return o.remote
}

// Hash returns "" since HTTP (in Go or OpenSSH) doesn't support remote calculation of hashes
func (o *Object) Hash(ctx context.Context, r hash.Type) (string, error) {
	return "", hash.ErrUnsupported
}

// Size returns the size in bytes of the remote http file
func (o *Object) Size() int64 {
	return o.size
}

// ModTime returns the modification time of the remote http file
func (o *Object) ModTime(ctx context.Context) time.Time {
	return o.modTime
}

// SetModTime sets the modification and access time to the specified time
//
// it also updates the info field
func (o *Object) SetModTime(ctx context.Context, modTime time.Time) error {
	return errorReadOnly
}

// Storable returns whether the remote http file is a regular file (not a directory, symbolic link, block device, character device, named pipe, etc.)
func (o *Object) Storable() bool {
	return true
}

func (o *Object) variation(ctx context.Context) (*api.Variation, error) {
	// TODO: could cache the result of GetDownload() for one hour, which is how the expiration
	// on the pre-signed s3 URLs. This function may be called by fetchMetadata() and Open().
	m, err := o.fs.gopro.GetDownload(ctx, o.mediaId)
	if err != nil {
		return nil, err
	}

	/** Get the URL for the variation we care about. GoPro returns something like
	 * {
	 * 	 "url": "...",
	 * 	 "head": "...",
	 * 	 "item_number": 2,
	 * 	 "label": "source",
	 * },
	 *
	 * when there are multiple items. Otherwise there will be a label="source" with no
	 * item_number. Indexing is not guaranteed since there may be a "concat" version from
	 * hitting "Request full length video" in the UI.
	 */
	for _, v := range m.Embedded.Variations {
		labelMatch := v.Label == "source" || // normal videos
			v.Label == "baked_source" // multi clip edits

		itemMatch := v.ItemNumber == nil || // only one part
			*v.ItemNumber == o.itemIdx // multi part

		if labelMatch && itemMatch {
			return &v, nil
		}
	}

	return nil, fmt.Errorf("no source URL found for media ID %s and item index %d", o.mediaId, o.itemIdx)
}

// Open a remote http file object for reading. Seek is supported
func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (in io.ReadCloser, err error) {
	variation, err := o.variation(ctx)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", variation.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("Open failed: %w", err)
	}

	// Add optional headers
	fs.OpenOptionAddHTTPHeaders(req.Header, options)

	// Do the request
	res, err := o.fs.httpClient.Do(req)
	err = statusError(res, err)
	if err != nil {
		return nil, fmt.Errorf("Open failed: %w", err)
	}

	if err = o.decodeMetadata(ctx, res); err != nil {
		return nil, fmt.Errorf("decodeMetadata failed: %w", err)
	}
	return res.Body, nil
}

// fetchMetadata fetches the download links and sends a HEAD request to update info fields in
// the Object
func (o *Object) fetchMetadata(ctx context.Context) error {
	fs.Debugf(o, "making head request to fetch metadata for media id %v", o.mediaId)
	variation, err := o.variation(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, variation.Head, nil)
	if err != nil {
		return fmt.Errorf("stat failed: %w", err)
	}

	res, err := o.fs.httpClient.Do(req)
	if err == nil && res.StatusCode == http.StatusNotFound {
		return fs.ErrorObjectNotFound
	}
	err = statusError(res, err)
	if err != nil {
		return fmt.Errorf("failed to stat: %w", err)
	}
	return o.decodeMetadata(ctx, res)
}

// decodeMetadata updates info fields in the Object according to HTTP response headers
func (o *Object) decodeMetadata(ctx context.Context, res *http.Response) error {
	t, err := http.ParseTime(res.Header.Get("Last-Modified"))
	if err != nil {
		t = timeUnset
	}
	o.modTime = t
	o.contentType = res.Header.Get("Content-Type")
	o.size = rest.ParseSizeFromHeaders(res.Header)
	return nil
}

// Hashes returns hash.HashNone to indicate remote hashing is unavailable
func (f *Fs) Hashes() hash.Set {
	return hash.Set(hash.None)
}

// Mkdir makes the root directory of the Fs object
func (f *Fs) Mkdir(ctx context.Context, dir string) error {
	return errorReadOnly
}

// Remove a remote http file object
func (o *Object) Remove(ctx context.Context) error {
	return errorReadOnly
}

// Rmdir removes the root directory of the Fs object
func (f *Fs) Rmdir(ctx context.Context, dir string) error {
	return errorReadOnly
}

// Update in to the object with the modTime given of the given size
func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	return errorReadOnly
}

// MimeType of an Object if known, "" otherwise
func (o *Object) MimeType(ctx context.Context) string {
	return o.contentType
}

// Check the interfaces are satisfied
var (
	_ fs.Fs = &Fs{}
	// _ fs.ListRer   = &Fs{}
	_ fs.Object    = &Object{}
	_ fs.MimeTyper = &Object{}
)
