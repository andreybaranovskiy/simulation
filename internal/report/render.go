package report

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// Renderer turns a print URL into a PDF. It holds no browser open between
// renders: a report is an occasional, heavy operation, and a fresh process per
// render is the clean isolation boundary, the same reasoning that gives every
// simulation run its own process.
type Renderer struct {
	browserPath string
	baseURL     string
	cookieName  string
	timeout     time.Duration
	log         *slog.Logger
}

// Options configures a renderer. BaseURL is where the print routes are reached,
// which is the server's own loopback address: the browser and the server are on
// the same machine, so a report never leaves it.
type Options struct {
	BrowserPath string
	BaseURL     string
	CookieName  string
	Timeout     time.Duration
	Log         *slog.Logger
}

// New builds a renderer, resolving the browser once so a misconfiguration is
// reported at startup rather than on the first export.
func New(opts Options) (*Renderer, error) {
	path, err := FindBrowser(opts.BrowserPath)
	if err != nil {
		return nil, err
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}

	log := opts.Log
	if log == nil {
		log = slog.Default()
	}

	log.Info("pdf export ready", "browser", path)

	return &Renderer{
		browserPath: path,
		baseURL:     opts.BaseURL,
		cookieName:  opts.CookieName,
		timeout:     timeout,
		log:         log,
	}, nil
}

// Job is one render: a print path within the SPA, a session token to load it
// with, and the header and footer text for the paper.
type Job struct {
	// Path is the print route, e.g. /print/report/{id}. It is loaded against
	// BaseURL, so it stays on the server's own origin.
	Path string
	// SessionToken authenticates the print route as the report's owner, so a
	// report can only ever contain data that owner is allowed to see.
	SessionToken string
	// Title and Footer are printed in the running header and footer. Title is
	// the report's name; Footer is typically the project and a date.
	Title  string
	Footer string
	// Landscape suits a wide comparison table; a single scenario reads better
	// in portrait.
	Landscape bool
}

// Render loads the print route in a headless browser and prints it to a PDF.
func (r *Renderer) Render(ctx context.Context, job Job) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(r.browserPath),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		// The browser and the data it prints are both ours, on loopback, so the
		// sandbox is disabled to survive running as a service account with no
		// desktop. It never loads anything but this server's own print routes.
		chromedp.NoSandbox,
		chromedp.Flag("hide-scrollbars", true),
	)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, allocOpts...)
	defer cancelAlloc()

	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx,
		chromedp.WithLogf(func(string, ...any) {}))
	defer cancelBrowser()

	url := r.baseURL + job.Path

	var pdf []byte
	err := chromedp.Run(browserCtx,
		network.Enable(),
		r.setSessionCookie(job.SessionToken),
		chromedp.Navigate(url),
		// The print page sets this flag once every section has its data and has
		// drawn. Waiting on a signal from the page, rather than a fixed sleep,
		// is what keeps a heavy comparison from printing half-rendered while
		// not making a light report wait needlessly.
		chromedp.Poll("window.__PRINT_READY__ === true", nil,
			chromedp.WithPollingTimeout(r.timeout)),
		chromedp.ActionFunc(func(ctx context.Context) error {
			data, _, err := printParams(job).Do(ctx)
			if err != nil {
				return fmt.Errorf("print to pdf: %w", err)
			}
			pdf = data
			return nil
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", job.Path, err)
	}

	return pdf, nil
}

// setSessionCookie plants the report owner's session on the browser before it
// navigates, so the SPA's own fetches carry it and RBAC applies unchanged.
func (r *Renderer) setSessionCookie(token string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		expires := cdp.TimeSinceEpoch(time.Now().Add(time.Hour))
		err := network.SetCookie(r.cookieName, token).
			WithURL(r.baseURL).
			WithExpires(&expires).
			WithHTTPOnly(true).
			Do(ctx)
		if err != nil {
			return fmt.Errorf("set session cookie: %w", err)
		}
		return nil
	})
}

// printParams builds the PDF settings: A4, backgrounds on so the charts keep
// their fills, and a running header and footer with a page number.
func printParams(job Job) *page.PrintToPDFParams {
	const (
		a4WidthIn  = 8.27
		a4HeightIn = 11.69
	)

	width, height := a4WidthIn, a4HeightIn
	if job.Landscape {
		width, height = a4HeightIn, a4WidthIn
	}

	return page.PrintToPDF().
		WithPrintBackground(true).
		WithLandscape(job.Landscape).
		WithPaperWidth(width).
		WithPaperHeight(height).
		WithMarginTop(0.55).
		WithMarginBottom(0.55).
		WithMarginLeft(0.5).
		WithMarginRight(0.5).
		WithDisplayHeaderFooter(true).
		WithHeaderTemplate(headerTemplate(job.Title)).
		WithFooterTemplate(footerTemplate(job.Footer))
}

// headerTemplate is the running header. Chrome supplies the styling context, so
// the font size has to be set inline and small, and the special classes it
// fills in (pageNumber, totalPages, title, date) are the only dynamic parts.
func headerTemplate(title string) string {
	return `<div style="font-size:8px; font-family:system-ui,sans-serif; color:#8a93b0;
		width:100%; padding:0 12mm; display:flex; justify-content:space-between;
		border-bottom:0.5px solid #d7dcec; padding-bottom:3px;">
		<span>` + html.EscapeString(title) + `</span>
		<span class="date"></span>
	</div>`
}

// footerTemplate carries the attribution and the page number, the one thing a
// printed report is expected to have and a screen never needs.
func footerTemplate(footer string) string {
	return `<div style="font-size:8px; font-family:system-ui,sans-serif; color:#8a93b0;
		width:100%; padding:0 12mm; display:flex; justify-content:space-between;">
		<span>` + html.EscapeString(footer) + `</span>
		<span>Page <span class="pageNumber"></span> of <span class="totalPages"></span></span>
	</div>`
}
