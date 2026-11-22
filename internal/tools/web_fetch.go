package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode"

	"omni-bot/internal/ai"
)

const (
	defaultFetchTimeout     = 15 * time.Second
	defaultFetchMaxBytes    = 2 << 20
	defaultFetchMaxChars    = 12000
	defaultFetchUserAgent   = "omni-bot/1.0"
	defaultMaxRedirectCount = 5
)

var ErrSSRFBlocked = errors.New("web_fetch: blocked private or loopback address")

// WebFetchConfig configures the built-in web_fetch tool.
type WebFetchConfig struct {
	HTTPClient           *http.Client
	Timeout              time.Duration
	MaxBodyBytes         int64
	MaxOutputChars       int
	AllowPrivateNetworks bool
	UserAgent            string
}

type webFetchTool struct {
	client               *http.Client
	customClient         bool
	maxBodyBytes         int64
	maxOutputChars       int
	allowPrivateNetworks bool
	userAgent            string
}

// NewWebFetchTool creates the built-in web_fetch tool with SSRF protection, size cap, and HTML main-text extraction.
func NewWebFetchTool(cfg WebFetchConfig) Tool {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultFetchTimeout
	}
	maxBytes := cfg.MaxBodyBytes
	if maxBytes <= 0 {
		maxBytes = defaultFetchMaxBytes
	}
	maxChars := cfg.MaxOutputChars
	if maxChars <= 0 {
		maxChars = defaultFetchMaxChars
	}
	ua := strings.TrimSpace(cfg.UserAgent)
	if ua == "" {
		ua = defaultFetchUserAgent
	}

	tool := &webFetchTool{
		maxBodyBytes:         maxBytes,
		maxOutputChars:       maxChars,
		allowPrivateNetworks: cfg.AllowPrivateNetworks,
		userAgent:            ua,
	}

	if cfg.HTTPClient != nil {
		tool.client = cfg.HTTPClient
		tool.customClient = true
	} else {
		dialer := &net.Dialer{Timeout: min(timeout, 10*time.Second)}
		transport := &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if !cfg.AllowPrivateNetworks {
					if err := validateDialTarget(ctx, addr); err != nil {
						return nil, err
					}
				}
				return dialer.DialContext(ctx, network, addr)
			},
		}
		tool.client = &http.Client{
			Timeout:   timeout,
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= defaultMaxRedirectCount {
					return errors.New("web_fetch: too many redirects")
				}
				return tool.validateURL(req.Context(), req.URL)
			},
		}
	}
	return tool
}

func (t *webFetchTool) Definition() ai.ToolDef {
	return ai.ToolDef{
		Name:        "web_fetch",
		Description: "Fetch an HTTP(S) web page and extract clean readable text (stripping scripts, styles, and navigation).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "HTTP or HTTPS URL to fetch.",
				},
				"max_chars": map[string]any{
					"type":        "integer",
					"description": "Optional character cap for extracted text.",
				},
			},
			"required": []string{"url"},
		},
	}
}

type webFetchArgs struct {
	URL      string `json:"url"`
	MaxChars int    `json:"max_chars"`
}

// WebFetchOutput is the structured payload returned by web_fetch.
type WebFetchOutput struct {
	URL         string `json:"url"`
	StatusCode  int    `json:"status_code"`
	ContentType string `json:"content_type"`
	Title       string `json:"title,omitempty"`
	Text        string `json:"text"`
	Truncated   bool   `json:"truncated"`
}

func (t *webFetchTool) Execute(ctx context.Context, argsJSON []byte) (Result, error) {
	var args webFetchArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return Result{}, fmt.Errorf("decode web_fetch args: %w", err)
	}
	rawURL := strings.TrimSpace(args.URL)
	if rawURL == "" {
		return Result{}, errors.New("web_fetch: url is required")
	}
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return Result{}, fmt.Errorf("web_fetch: invalid url: %w", err)
	}
	if err := t.validateURL(ctx, parsedURL); err != nil {
		return Result{}, err
	}

	maxChars := t.maxOutputChars
	if args.MaxChars > 0 && args.MaxChars < maxChars {
		maxChars = args.MaxChars
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return Result{}, fmt.Errorf("web_fetch: build request: %w", err)
	}
	req.Header.Set("User-Agent", t.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json;q=0.9,*/*;q=0.5")

	resp, err := t.client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("web_fetch: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("web_fetch: HTTP %d from %s", resp.StatusCode, resp.Request.URL.String())
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, t.maxBodyBytes+1))
	if err != nil {
		return Result{}, fmt.Errorf("web_fetch: read body: %w", err)
	}
	bodyTruncated := false
	if int64(len(body)) > t.maxBodyBytes {
		body = body[:t.maxBodyBytes]
		bodyTruncated = true
	}

	contentType := resp.Header.Get("Content-Type")
	var title, text string
	if isHTMLContent(contentType, body) {
		title, text = extractHTMLText(string(body))
	} else {
		text = normalizeWhitespace(string(body))
	}

	text, charTruncated := truncateRunes(text, maxChars)
	truncated := bodyTruncated || charTruncated

	finalURL := parsedURL.String()
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}

	out := WebFetchOutput{
		URL:         finalURL,
		StatusCode:  resp.StatusCode,
		ContentType: contentType,
		Title:       title,
		Text:        text,
		Truncated:   truncated,
	}

	var sb strings.Builder
	if title != "" {
		sb.WriteString("Title: ")
		sb.WriteString(title)
		sb.WriteByte('\n')
	}
	sb.WriteString("URL: ")
	sb.WriteString(finalURL)
	sb.WriteString("\n\n")
	sb.WriteString(text)
	if truncated {
		sb.WriteString("\n\n[truncated]")
	}
	return Result{
		Text:       sb.String(),
		Structured: out,
	}, nil
}

func (t *webFetchTool) validateURL(ctx context.Context, u *url.URL) error {
	if u == nil {
		return errors.New("web_fetch: empty url")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("web_fetch: unsupported scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("web_fetch: missing host")
	}
	if t.allowPrivateNetworks {
		return nil
	}
	return checkHostAllowed(ctx, host)
}

func validateDialTarget(ctx context.Context, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return checkHostAllowed(ctx, host)
}

func checkHostAllowed(ctx context.Context, host string) error {
	lower := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return fmt.Errorf("%w: %s", ErrSSRFBlocked, host)
	}
	if ip, err := netip.ParseAddr(lower); err == nil {
		if isDisallowedAddr(ip) {
			return fmt.Errorf("%w: %s", ErrSSRFBlocked, host)
		}
		return nil
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("web_fetch: resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("web_fetch: no IP addresses for %q", host)
	}
	for _, addr := range addrs {
		if isDisallowedAddr(addr) {
			return fmt.Errorf("%w: %s resolved to %s", ErrSSRFBlocked, host, addr.String())
		}
	}
	return nil
}

var cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")

func isDisallowedAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return !addr.IsValid() ||
		addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified() ||
		cgnatPrefix.Contains(addr)
}

func isHTMLContent(contentType string, body []byte) bool {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml") {
		return true
	}
	if ct != "" {
		return false
	}
	head := strings.ToLower(strings.TrimSpace(string(body[:min(len(body), 512)])))
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html")
}

// extractHTMLText extracts page title and readable main text while dropping script, style, nav, and boilerplate tags.
func extractHTMLText(raw string) (string, string) {
	title := extractTagInner(raw, "title")
	title = normalizeInlineWhitespace(html.UnescapeString(stripTagsSimple(title)))

	target := raw
	if mainBody := extractTagInner(raw, "main"); len(strings.TrimSpace(mainBody)) >= 40 {
		target = mainBody
	} else if articleBody := extractTagInner(raw, "article"); len(strings.TrimSpace(articleBody)) >= 40 {
		target = articleBody
	}

	var out strings.Builder
	skipDepth := map[string]int{}
	inSkip := func() bool {
		for _, d := range skipDepth {
			if d > 0 {
				return true
			}
		}
		return false
	}

	i := 0
	n := len(target)
	for i < n {
		if target[i] != '<' {
			next := strings.IndexByte(target[i:], '<')
			var chunk string
			if next < 0 {
				chunk = target[i:]
				i = n
			} else {
				chunk = target[i : i+next]
				i += next
			}
			if !inSkip() {
				out.WriteString(chunk)
			}
			continue
		}

		// HTML comment: <!-- ... -->
		if strings.HasPrefix(target[i:], "<!--") {
			end := strings.Index(target[i+4:], "-->")
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}

		// Find end of tag, respecting quoted attributes.
		tagEnd := findTagEnd(target, i+1)
		if tagEnd < 0 {
			break
		}
		tagContent := strings.TrimSpace(target[i+1 : tagEnd])
		i = tagEnd + 1
		if tagContent == "" || tagContent[0] == '!' || tagContent[0] == '?' {
			continue
		}

		closing := false
		if tagContent[0] == '/' {
			closing = true
			tagContent = strings.TrimSpace(tagContent[1:])
		}
		selfClosing := strings.HasSuffix(tagContent, "/")
		tagName := parseTagName(tagContent)
		if tagName == "" {
			continue
		}

		// Raw-text elements (<script>, <style>) cannot nest and end only on their matching close tag.
		if !closing && !selfClosing && isRawSkipTag(tagName) {
			closeMarker := "</" + tagName
			lowerRest := strings.ToLower(target[i:])
			if idx := strings.Index(lowerRest, closeMarker); idx >= 0 {
				closeEnd := strings.IndexByte(target[i+idx:], '>')
				if closeEnd >= 0 {
					i += idx + closeEnd + 1
					continue
				}
			}
			break
		}

		if isNestedSkipTag(tagName) {
			if closing {
				if skipDepth[tagName] > 0 {
					skipDepth[tagName]--
				}
			} else if !selfClosing {
				skipDepth[tagName]++
			}
			continue
		}

		if !inSkip() && isBlockTag(tagName) {
			out.WriteByte('\n')
		}
	}

	decoded := html.UnescapeString(out.String())
	return title, normalizeWhitespace(decoded)
}

func findTagEnd(s string, start int) int {
	var quote byte
	for i := start; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == '>' {
			return i
		}
	}
	return -1
}

func parseTagName(tagContent string) string {
	end := 0
	for end < len(tagContent) {
		c := tagContent[end]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			end++
			continue
		}
		break
	}
	return strings.ToLower(tagContent[:end])
}

func isRawSkipTag(tag string) bool {
	switch tag {
	case "script", "style", "noscript", "svg":
		return true
	default:
		return false
	}
}

func isNestedSkipTag(tag string) bool {
	switch tag {
	case "nav", "footer", "aside", "iframe", "template":
		return true
	default:
		return false
	}
}

func isBlockTag(tag string) bool {
	switch tag {
	case "p", "div", "section", "article", "main", "header", "br", "hr",
		"li", "ul", "ol", "tr", "td", "th", "table", "blockquote", "pre",
		"h1", "h2", "h3", "h4", "h5", "h6":
		return true
	default:
		return false
	}
}

func extractTagInner(raw, tag string) string {
	lower := strings.ToLower(raw)
	openPrefix := "<" + tag
	idx := 0
	for {
		pos := strings.Index(lower[idx:], openPrefix)
		if pos < 0 {
			return ""
		}
		pos += idx
		after := pos + len(openPrefix)
		if after < len(lower) {
			c := lower[after]
			if c != '>' && c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				idx = after
				continue
			}
		}
		gt := findTagEnd(raw, after)
		if gt < 0 {
			return ""
		}
		closeTag := "</" + tag + ">"
		end := strings.Index(lower[gt+1:], closeTag)
		if end < 0 {
			return ""
		}
		return raw[gt+1 : gt+1+end]
	}
}

func stripTagsSimple(s string) string {
	var sb strings.Builder
	inTag := false
	for i := range len(s) {
		switch s[i] {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				sb.WriteByte(s[i])
			}
		}
	}
	return sb.String()
}

func normalizeInlineWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func normalizeWhitespace(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	var cleaned []string
	blankRun := 0
	for _, line := range lines {
		norm := collapseSpaces(line)
		if norm == "" {
			if len(cleaned) > 0 && blankRun == 0 {
				cleaned = append(cleaned, "")
			}
			blankRun++
			continue
		}
		blankRun = 0
		cleaned = append(cleaned, norm)
	}
	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}

func collapseSpaces(s string) string {
	var sb strings.Builder
	prevSpace := true
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace {
				sb.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		sb.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimSpace(sb.String())
}

func truncateRunes(s string, maxRunes int) (string, bool) {
	if maxRunes <= 0 {
		return s, false
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s, false
	}
	return strings.TrimSpace(string(runes[:maxRunes])), true
}
