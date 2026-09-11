package manager

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// OpenCode bills the two gateways differently, and the official docs are the
// source of truth for both:
//
//   - Zen (https://opencode.ai/docs/zen/) is pay-as-you-go: each model has a
//     published price per 1M tokens, the account balance auto-reloads when it
//     drops below a threshold, and the workspace or member can cap monthly
//     spend in USD.
//   - Go (https://opencode.ai/docs/go/) is a $10/month subscription whose usage
//     limits are expressed as a monthly USD amount per model, split across three
//     windows: 5 hours is 20% of the monthly limit, a week is 50%, and the month
//     is 100%. Published token prices are what turn tokens into that USD amount,
//     which is why the same catalog prices Go usage instead of a vendor table.
//
// The doc tables carry per-model data that no other source exposes: the Go
// monthly allowance, the estimated request counts per window, the official model
// ids with their endpoints, and Zen's deprecation dates. They are parsed by table
// shape rather than by header text so an English or localized page, or a reordered
// column, still yields the same rows.

const (
	openCodeGoDocsURL    = "https://opencode.ai/docs/go/"
	openCodeZenDocsURL   = "https://opencode.ai/docs/zen/"
	openCodeDocsSource   = "opencode.ai official pricing docs"
	openCodeDocsTimeout  = 30 * time.Second
	openCodeDocsMaxBytes = 8 << 20

	// Go splits its monthly USD allowance across three windows. These are the
	// documented defaults; the live documentation is parsed for the real numbers so
	// a change on the OpenCode side is picked up by the next sync.
	openCodeGoFiveHourFraction = 0.2
	openCodeGoWeeklyFraction   = 0.5
	openCodeGoSubscriptionUSD  = 10

	// Zen auto-reload defaults, likewise replaced by the parsed documentation.
	openCodeZenAutoReloadBelowUSD = 5
	openCodeZenAutoReloadUSD      = 20
)

// OpenCodeBillingMode describes how one gateway charges for usage. The values are
// static because they are contractual rather than measured, but they are exported
// with the catalog so the UI can label usage correctly when the model changes.
type OpenCodeBillingMode struct {
	Kind               string  `json:"kind"`
	Metered            bool    `json:"metered"`
	SubscriptionUSD    float64 `json:"subscription_usd_per_month,omitempty"`
	MonthlyLimitUSD    float64 `json:"monthly_limit_usd,omitempty"`
	FiveHourFraction   float64 `json:"five_hour_fraction,omitempty"`
	WeeklyFraction     float64 `json:"weekly_fraction,omitempty"`
	DocsURL            string  `json:"docs_url,omitempty"`
	Summary            string  `json:"summary,omitempty"`
	AutoReloadBelowUSD float64 `json:"auto_reload_below_usd,omitempty"`
	AutoReloadUSD      float64 `json:"auto_reload_usd,omitempty"`
	// ParsedFromDocs records that the numbers came from the live documentation
	// rather than the built-in defaults, so the UI can say where they came from.
	ParsedFromDocs bool `json:"parsed_from_docs,omitempty"`
}

// openCodeBillingModes returns the documented billing descriptors. The strings are
// operator-facing and intentionally short: the UI renders the numbers itself.
func openCodeBillingModes() []OpenCodeBillingMode {
	return []OpenCodeBillingMode{
		{
			Kind:               openCodeKindZenValue,
			Metered:            true,
			MonthlyLimitUSD:    0,
			DocsURL:            openCodeZenDocsURL,
			Summary:            "Pay-as-you-go per 1M tokens; balance auto-reloads when it drops below a threshold and a monthly workspace limit can cap spend.",
			AutoReloadBelowUSD: openCodeZenAutoReloadBelowUSD,
			AutoReloadUSD:      openCodeZenAutoReloadUSD,
		},
		{
			Kind:             openCodeKindGoValue,
			Metered:          false,
			SubscriptionUSD:  10,
			FiveHourFraction: openCodeGoFiveHourFraction,
			WeeklyFraction:   openCodeGoWeeklyFraction,
			DocsURL:          openCodeGoDocsURL,
			Summary:          "$10/month subscription; each model has a monthly USD allowance split 20% / 50% / 100% across 5-hour, weekly and monthly windows.",
		},
	}
}

// openCodeEstimatedRequests is the documented request estimate for one model in
// one Go window.
type openCodeEstimatedRequests struct {
	FiveHour int64 `json:"five_hour,omitempty"`
	Weekly   int64 `json:"weekly,omitempty"`
	Monthly  int64 `json:"monthly,omitempty"`
}

// openCodeDocsModel is one parsed documentation row set for a single model.
type openCodeDocsModel struct {
	ID          string
	Name        string
	Endpoint    string
	Deprecated  string
	Prices      map[string]float64
	MonthlyUSD  float64
	HasMonthly  bool
	Estimates   openCodeEstimatedRequests
	HasEstimate bool
}

// openCodeDocsCatalog is the parsed view of one gateway's documentation.
type openCodeDocsCatalog struct {
	Models map[string]openCodeDocsModel
	// Billing carries the billing parameters found in the documentation prose.
	// A zero value means the page did not state them and the defaults apply.
	Billing OpenCodeBillingMode
	// BillingParsed records whether Billing held any parsed value.
	BillingParsed bool
}

//go:embed opencode_docs_snapshot.json
var embeddedOpenCodeDocsJSON []byte

// openCodeDocsFetcher fetches one documentation page with a bounded read.
type openCodeDocsFetcher struct {
	client *http.Client
}

func newOpenCodeDocsFetcher(client *http.Client) openCodeDocsFetcher {
	if client == nil {
		client = &http.Client{Timeout: openCodeDocsTimeout}
	}
	return openCodeDocsFetcher{client: client}
}

func (f openCodeDocsFetcher) fetch(ctx context.Context, rawURL string) ([]byte, error) {
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if errRequest != nil {
		return nil, errRequest
	}
	request.Header.Set("Accept", "text/html")
	response, errDo := f.client.Do(request)
	if errDo != nil {
		return nil, fmt.Errorf("OpenCode pricing docs could not be fetched")
	}
	if response == nil || response.Body == nil {
		return nil, fmt.Errorf("OpenCode pricing docs returned an empty response")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenCode pricing docs returned HTTP status %d", response.StatusCode)
	}
	body, errRead := io.ReadAll(io.LimitReader(response.Body, openCodeDocsMaxBytes+1))
	if errRead != nil {
		return nil, fmt.Errorf("OpenCode pricing docs could not be read")
	}
	if int64(len(body)) > openCodeDocsMaxBytes {
		return nil, fmt.Errorf("OpenCode pricing docs response is too large")
	}
	return body, nil
}

var (
	openCodeDocsTablePattern  = regexp.MustCompile(`(?is)<table.*?</table>`)
	openCodeDocsRowPattern    = regexp.MustCompile(`(?is)<tr.*?</tr>`)
	openCodeDocsCellPattern   = regexp.MustCompile(`(?is)<t[dh][^>]*>(.*?)</t[dh]>`)
	openCodeDocsTagPattern    = regexp.MustCompile(`(?s)<[^>]*>`)
	openCodeDocsSpacePattern  = regexp.MustCompile(`\s+`)
	openCodeDocModelIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	openCodeDocNumberPattern  = regexp.MustCompile(`^[0-9][0-9,]*$`)
	openCodeDocMonthPattern   = regexp.MustCompile(`(?i)^(january|february|march|april|may|june|july|august|september|october|november|december|[0-9]{4}-[0-9]{2}-[0-9]{2})`)
)

// parseOpenCodeDocsCatalog extracts the pricing, allowance, model-id and
// estimation tables for one gateway. Rows that do not match any known table shape
// are ignored, so an unrelated documentation table is never priced.
func parseOpenCodeDocsCatalog(raw []byte) openCodeDocsCatalog {
	return parseOpenCodeDocsCatalogFor(raw, "")
}

// parseOpenCodeDocsCatalogFor parses one gateway's documentation. When kind is
// set, the billing prose is read as well.
func parseOpenCodeDocsCatalogFor(raw []byte, kind string) openCodeDocsCatalog {
	catalog := openCodeDocsCatalog{Models: map[string]openCodeDocsModel{}}
	byName := map[string]string{}
	ensure := func(name string) (string, *openCodeDocsModel) {
		name = strings.TrimSpace(name)
		if name == "" {
			return "", nil
		}
		key := normalizeOpenCodePriceName(name)
		if key == "" {
			return "", nil
		}
		if existing, ok := byName[key]; ok {
			model := catalog.Models[existing]
			return existing, &model
		}
		id := key
		byName[key] = id
		catalog.Models[id] = openCodeDocsModel{ID: id, Name: name}
		model := catalog.Models[id]
		return id, &model
	}
	store := func(id string, model openCodeDocsModel) {
		if id == "" {
			return
		}
		catalog.Models[id] = model
	}

	for _, table := range openCodeDocsTablePattern.FindAll(raw, -1) {
		rows := make([][]string, 0, 8)
		for _, row := range openCodeDocsRowPattern.FindAll(table, -1) {
			cells := make([]string, 0, 6)
			for _, cell := range openCodeDocsCellPattern.FindAllSubmatch(row, -1) {
				cells = append(cells, cleanOpenCodeDocsCell(string(cell[1])))
			}
			if len(cells) > 0 {
				rows = append(rows, cells)
			}
		}
		for _, cells := range rows {
			if len(cells) == 0 {
				continue
			}
			// The shape decides whether the row is a model row at all, so an
			// unrelated table never creates a phantom model.
			kind := openCodeDocsRowKind(cells)
			if kind == openCodeDocsRowNone {
				continue
			}
			key, target := ensure(cells[0])
			if key == "" || target == nil {
				continue
			}
			model := *target
			switch kind {
			case openCodeDocsRowIdentity:
				if id := strings.TrimSpace(cells[1]); id != "" {
					delete(catalog.Models, model.ID)
					model.ID = id
					byName[normalizeOpenCodePriceName(model.Name)] = id
					key = id
				}
				model.Endpoint = strings.TrimSpace(cells[2])
			case openCodeDocsRowGoPrice:
				prices, ok := parseOpenCodeDocsPrices(cells[1:5])
				if !ok {
					continue
				}
				limit, hasLimit := parseOpenCodeDocsUSD(cells[5])
				model.Prices = prices
				model.MonthlyUSD, model.HasMonthly = limit, hasLimit
			case openCodeDocsRowPrice:
				prices, ok := parseOpenCodeDocsPrices(cells[1:5])
				if !ok {
					continue
				}
				model.Prices = prices
			case openCodeDocsRowEstimates:
				model.Estimates = openCodeEstimatedRequests{
					FiveHour: parseOpenCodeDocsCount(cells[1]),
					Weekly:   parseOpenCodeDocsCount(cells[2]),
					Monthly:  parseOpenCodeDocsCount(cells[3]),
				}
				model.HasEstimate = true
			case openCodeDocsRowDeprecation:
				model.Deprecated = strings.TrimSpace(cells[1])
			}
			store(key, model)
		}
	}
	if strings.TrimSpace(kind) != "" {
		catalog.Billing, catalog.BillingParsed = parseOpenCodeDocsBilling(openCodeDocsText(raw), kind)
	}
	return catalog
}

// openCodeDocsText renders the page as plain text so the contract sentences that
// are not inside a table can be read. Markup is stripped and whitespace collapsed.
func openCodeDocsText(raw []byte) string {
	text := openCodeDocsTagPattern.ReplaceAllString(string(raw), " ")
	text = html.UnescapeString(text)
	return openCodeDocsSpacePattern.ReplaceAllString(text, " ")
}

var (
	openCodeSubscriptionPattern     = regexp.MustCompile(`\x24\s*([0-9]+(?:\.[0-9]+)?)\s*(?:/|\s*per\s*|\x{6bcf})\s*(?:month|\x{6708})`)
	openCodeFiveHourStartPattern    = regexp.MustCompile(`(?:5\s*(?:-|\s)?\s*hour|5\s*\x{5c0f}\x{65f6})`)
	openCodePercentPattern          = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*%`)
	openCodeAutoReloadBelowPattern  = regexp.MustCompile(`(?:below|\x{4f4e}\x{4e8e}|under)\s*\x{24}\s*([0-9]+(?:\.[0-9]+)?)`)
	openCodeAutoReloadAmountPattern = regexp.MustCompile(`(?:reload|\x{5145}\x{503c}|recharge)\s*\x{24}\s*([0-9]+(?:\.[0-9]+)?)`)
)

// parseOpenCodeDocsBilling reads the contract numbers out of the documentation
// prose: the Go subscription price and its 5-hour/weekly/monthly split, and Zen's
// auto-reload threshold and amount. Values that are absent or implausible keep the
// built-in defaults, so a documentation rewrite degrades instead of mispricing.
func parseOpenCodeDocsBilling(text, kind string) (OpenCodeBillingMode, bool) {
	mode := OpenCodeBillingMode{Kind: kind}
	if strings.EqualFold(strings.TrimSpace(kind), openCodeKindZenValue) {
		mode.Metered = true
		mode.DocsURL = openCodeZenDocsURL
		mode.Summary = openCodeBillingModes()[0].Summary
		mode.AutoReloadBelowUSD = openCodeZenAutoReloadBelowUSD
		mode.AutoReloadUSD = openCodeZenAutoReloadUSD
		parsed := false
		if match := openCodeAutoReloadBelowPattern.FindStringSubmatch(text); len(match) == 2 {
			if value, ok := parseOpenCodeDocsUSD(match[1]); ok && value > 0 && value <= 1000 {
				mode.AutoReloadBelowUSD = value
				parsed = true
			}
		}
		if match := openCodeAutoReloadAmountPattern.FindStringSubmatch(text); len(match) == 2 {
			if value, ok := parseOpenCodeDocsUSD(match[1]); ok && value > 0 && value <= 10000 {
				mode.AutoReloadUSD = value
				parsed = true
			}
		}
		mode.ParsedFromDocs = parsed
		return mode, parsed
	}

	mode.DocsURL = openCodeGoDocsURL
	mode.Summary = openCodeBillingModes()[1].Summary
	mode.SubscriptionUSD = openCodeGoSubscriptionUSD
	mode.FiveHourFraction = openCodeGoFiveHourFraction
	mode.WeeklyFraction = openCodeGoWeeklyFraction
	if match := openCodeSubscriptionPattern.FindStringSubmatch(text); len(match) == 2 {
		if value, ok := parseOpenCodeDocsUSD(match[1]); ok && value > 0 && value <= 1000 {
			mode.SubscriptionUSD = value
		}
	}
	// The documented sentence enumerates the three windows in order:
	// "5-hour: 20% of the monthly limit; weekly: 50%; and monthly: 100%".
	// Reading the first three percentages after the 5-hour token avoids matching
	// the words "monthly limit" that appear before the monthly percentage itself.
	parsed := false
	if fiveHour, weekly, monthly, ok := openCodeDocsWindowSplit(text); ok {
		mode.FiveHourFraction = fiveHour / monthly
		mode.WeeklyFraction = weekly / monthly
		parsed = true
	}
	mode.ParsedFromDocs = parsed
	return mode, parsed
}

// openCodeDocsWindowSplit reads the 5-hour, weekly and monthly percentages from the
// documented enumeration. The three values must be stated in order and must not
// decrease, so an unrelated percentage cannot be mistaken for a window share.
func openCodeDocsWindowSplit(text string) (float64, float64, float64, bool) {
	location := openCodeFiveHourStartPattern.FindStringIndex(text)
	if location == nil {
		return 0, 0, 0, false
	}
	window := text[location[0]:]
	if len(window) > 400 {
		window = window[:400]
	}
	values := make([]float64, 0, 4)
	for _, match := range openCodePercentPattern.FindAllStringSubmatch(window, -1) {
		value, errParse := strconv.ParseFloat(match[1], 64)
		if errParse != nil || value <= 0 || value > 100 {
			continue
		}
		values = append(values, value)
		if len(values) == 3 {
			break
		}
	}
	if len(values) < 3 {
		return 0, 0, 0, false
	}
	fiveHour, weekly, monthly := values[0], values[1], values[2]
	if monthly <= 0 || fiveHour > weekly || weekly > monthly {
		return 0, 0, 0, false
	}
	return fiveHour, weekly, monthly, true
}

// openCodeDocsShape classifies one documentation row by its shape. Header rows
// and unrelated tables fall through to openCodeDocsRowNone.
type openCodeDocsShape int

const (
	openCodeDocsRowNone openCodeDocsShape = iota
	openCodeDocsRowIdentity
	openCodeDocsRowGoPrice
	openCodeDocsRowPrice
	openCodeDocsRowEstimates
	openCodeDocsRowDeprecation
)

func openCodeDocsRowKind(cells []string) openCodeDocsShape {
	if len(cells) == 0 {
		return openCodeDocsRowNone
	}
	switch {
	case len(cells) == 4 && openCodeDocModelIDPattern.MatchString(cells[1]) && strings.HasPrefix(cells[2], "https://"):
		return openCodeDocsRowIdentity
	case len(cells) == 6 && isOpenCodeDocsPriceRow(cells[1:5]):
		return openCodeDocsRowGoPrice
	case len(cells) == 5 && isOpenCodeDocsPriceRow(cells[1:5]):
		return openCodeDocsRowPrice
	case len(cells) == 4 && isOpenCodeDocsRequestRow(cells[1:4]):
		return openCodeDocsRowEstimates
	case len(cells) == 2 && openCodeDocMonthPattern.MatchString(cells[1]):
		return openCodeDocsRowDeprecation
	}
	return openCodeDocsRowNone
}

// isOpenCodeDocsPriceRow reports whether four cells look like price columns, which
// keeps a documentation table with a different meaning from being read as prices.
func isOpenCodeDocsPriceRow(cells []string) bool {
	_, ok := parseOpenCodeDocsPrices(cells)
	return ok
}

func isOpenCodeDocsRequestRow(cells []string) bool {
	if len(cells) != 3 {
		return false
	}
	for _, cell := range cells {
		if !openCodeDocNumberPattern.MatchString(strings.TrimSpace(cell)) {
			return false
		}
	}
	return true
}

// parseOpenCodeDocsPrices reads the four price cells. "Free" is a real zero price,
// while a dash or an empty cell means the dimension is not billed separately.
func parseOpenCodeDocsPrices(cells []string) (map[string]float64, bool) {
	if len(cells) != 4 {
		return nil, false
	}
	prices := map[string]float64{}
	recognized := 0
	for index, cell := range cells {
		key := []string{"input", "output", "cache_read", "cache_write"}[index]
		trimmed := strings.TrimSpace(cell)
		if trimmed == "" {
			continue
		}
		if isOpenCodeDocsFree(trimmed) {
			prices[key] = 0
			recognized++
			continue
		}
		if isOpenCodeDocsEmptyPrice(trimmed) {
			continue
		}
		value, ok := parseOpenCodeDocsUSD(trimmed)
		if !ok {
			return nil, false
		}
		prices[key] = value
		recognized++
	}
	return prices, recognized > 0
}

func isOpenCodeDocsFree(cell string) bool {
	switch strings.ToLower(strings.TrimSpace(cell)) {
	case "free", "\u514d\u8d39":
		return true
	}
	return false
}

func isOpenCodeDocsEmptyPrice(cell string) bool {
	switch strings.TrimSpace(cell) {
	case "-", "\u2014", "\u2013", "n/a", "N/A":
		return true
	}
	return false
}

func parseOpenCodeDocsUSD(cell string) (float64, bool) {
	trimmed := strings.TrimSpace(cell)
	if trimmed == "" {
		return 0, false
	}
	if isOpenCodeDocsFree(trimmed) {
		return 0, true
	}
	if isOpenCodeDocsEmptyPrice(trimmed) {
		return 0, false
	}
	trimmed = strings.TrimPrefix(trimmed, "$")
	trimmed = strings.ReplaceAll(trimmed, ",", "")
	value, errParse := strconv.ParseFloat(trimmed, 64)
	if errParse != nil || value < 0 {
		return 0, false
	}
	return value, true
}

func parseOpenCodeDocsCount(cell string) int64 {
	trimmed := strings.ReplaceAll(strings.TrimSpace(cell), ",", "")
	value, errParse := strconv.ParseInt(trimmed, 10, 64)
	if errParse != nil || value < 0 {
		return 0
	}
	return value
}

func cleanOpenCodeDocsCell(cell string) string {
	cleaned := openCodeDocsTagPattern.ReplaceAllString(cell, " ")
	cleaned = html.UnescapeString(cleaned)
	cleaned = openCodeDocsSpacePattern.ReplaceAllString(cleaned, " ")
	return strings.TrimSpace(cleaned)
}

// normalizeOpenCodePriceName folds a display name so the doc tables, the model-id
// table and the models.dev catalog can be joined on the same model.
func normalizeOpenCodePriceName(name string) string {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	if trimmed == "" {
		return ""
	}
	var builder strings.Builder
	builder.Grow(len(trimmed))
	lastDash := false
	for _, symbol := range trimmed {
		switch {
		case symbol >= 'a' && symbol <= 'z', symbol >= '0' && symbol <= '9', unicode.Is(unicode.Han, symbol):
			builder.WriteRune(symbol)
			lastDash = false
		default:
			if !lastDash && builder.Len() > 0 {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(builder.String(), "-")
}

// openCodeDocsSnapshotModel is the persisted shape of one parsed doc row.
type openCodeDocsSnapshotModel struct {
	ID         string                    `json:"id"`
	Name       string                    `json:"name,omitempty"`
	Endpoint   string                    `json:"endpoint,omitempty"`
	Deprecated string                    `json:"deprecated_at,omitempty"`
	Prices     map[string]float64        `json:"prices,omitempty"`
	MonthlyUSD float64                   `json:"monthly_limit_usd,omitempty"`
	Estimates  openCodeEstimatedRequests `json:"estimated_requests,omitempty"`
}

type openCodeDocsSnapshot struct {
	Source     string                               `json:"source"`
	Go         map[string]openCodeDocsSnapshotModel `json:"go,omitempty"`
	Zen        map[string]openCodeDocsSnapshotModel `json:"zen,omitempty"`
	GoBilling  *OpenCodeBillingMode                 `json:"go_billing,omitempty"`
	ZenBilling *OpenCodeBillingMode                 `json:"zen_billing,omitempty"`
}

func (c openCodeDocsCatalog) snapshot() map[string]openCodeDocsSnapshotModel {
	rows := make(map[string]openCodeDocsSnapshotModel, len(c.Models))
	for id, model := range c.Models {
		if len(model.Prices) == 0 && !model.HasMonthly && !model.HasEstimate && model.Endpoint == "" && model.Deprecated == "" {
			continue
		}
		rows[id] = openCodeDocsSnapshotModel{
			ID:         model.ID,
			Name:       model.Name,
			Endpoint:   model.Endpoint,
			Deprecated: model.Deprecated,
			Prices:     model.Prices,
			MonthlyUSD: model.MonthlyUSD,
			Estimates:  model.Estimates,
		}
	}
	return rows
}

func openCodeDocsCatalogFromSnapshot(rows map[string]openCodeDocsSnapshotModel) openCodeDocsCatalog {
	catalog := openCodeDocsCatalog{Models: make(map[string]openCodeDocsModel, len(rows))}
	for id, row := range rows {
		catalog.Models[id] = openCodeDocsModel{
			ID:          firstNonEmpty(strings.TrimSpace(row.ID), id),
			Name:        row.Name,
			Endpoint:    row.Endpoint,
			Deprecated:  row.Deprecated,
			Prices:      row.Prices,
			MonthlyUSD:  row.MonthlyUSD,
			HasMonthly:  row.MonthlyUSD > 0,
			Estimates:   row.Estimates,
			HasEstimate: row.Estimates.Monthly > 0 || row.Estimates.Weekly > 0 || row.Estimates.FiveHour > 0,
		}
	}
	return catalog
}

// parseEmbeddedOpenCodeDocs reads the embedded official snapshot so per-model
// allowances and request estimates exist before the first network sync.
// openCodeDocsBillingOrDefaults returns the parsed billing modes, keeping the
// documented defaults for a gateway the documentation did not describe.
func openCodeDocsBillingOrDefaults(goDocs, zenDocs openCodeDocsCatalog) []OpenCodeBillingMode {
	defaults := openCodeBillingModes()
	modes := make([]OpenCodeBillingMode, 0, len(defaults))
	for _, fallback := range defaults {
		parsed := openCodeDocsCatalog{}
		switch fallback.Kind {
		case openCodeKindGoValue:
			parsed = goDocs
		case openCodeKindZenValue:
			parsed = zenDocs
		}
		if parsed.BillingParsed {
			mode := parsed.Billing
			mode.Kind = fallback.Kind
			if mode.DocsURL == "" {
				mode.DocsURL = fallback.DocsURL
			}
			if mode.Summary == "" {
				mode.Summary = fallback.Summary
			}
			if !mode.Metered {
				if mode.SubscriptionUSD == 0 {
					mode.SubscriptionUSD = fallback.SubscriptionUSD
				}
				if mode.FiveHourFraction == 0 {
					mode.FiveHourFraction = fallback.FiveHourFraction
				}
				if mode.WeeklyFraction == 0 {
					mode.WeeklyFraction = fallback.WeeklyFraction
				}
			}
			if mode.Metered {
				if mode.AutoReloadBelowUSD == 0 {
					mode.AutoReloadBelowUSD = fallback.AutoReloadBelowUSD
				}
				if mode.AutoReloadUSD == 0 {
					mode.AutoReloadUSD = fallback.AutoReloadUSD
				}
			}
			modes = append(modes, mode)
			continue
		}
		modes = append(modes, fallback)
	}
	return modes
}

func parseEmbeddedOpenCodeDocs() (openCodeDocsCatalog, openCodeDocsCatalog, error) {
	if len(bytes.TrimSpace(embeddedOpenCodeDocsJSON)) == 0 {
		return openCodeDocsCatalog{}, openCodeDocsCatalog{}, fmt.Errorf("embedded OpenCode docs snapshot is empty")
	}
	var snapshot openCodeDocsSnapshot
	if errDecode := json.Unmarshal(embeddedOpenCodeDocsJSON, &snapshot); errDecode != nil {
		return openCodeDocsCatalog{}, openCodeDocsCatalog{}, fmt.Errorf("embedded OpenCode docs snapshot is invalid")
	}
	goDocs := openCodeDocsCatalogFromSnapshot(snapshot.Go)
	zenDocs := openCodeDocsCatalogFromSnapshot(snapshot.Zen)
	if snapshot.GoBilling != nil {
		goDocs.Billing, goDocs.BillingParsed = *snapshot.GoBilling, true
	}
	if snapshot.ZenBilling != nil {
		zenDocs.Billing, zenDocs.BillingParsed = *snapshot.ZenBilling, true
	}
	return goDocs, zenDocs, nil
}

// openCodeDocsEqual compares two parsed documentation catalogs by content, so a
// revalidation that produced the same tables is not reported as a change.
func openCodeDocsEqual(left, right openCodeDocsCatalog) bool {
	if len(left.Models) != len(right.Models) {
		return false
	}
	encode := func(catalog openCodeDocsCatalog) string {
		encoded, errEncode := json.Marshal(catalog.snapshot())
		if errEncode != nil {
			return ""
		}
		return string(encoded)
	}
	return encode(left) == encode(right)
}

// openCodeDocsModelIDs lists the official model ids from a parsed catalog.
func (c openCodeDocsCatalog) openCodeDocsModelIDs() []string {
	ids := make([]string, 0, len(c.Models))
	for id := range c.Models {
		if strings.TrimSpace(id) != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
