package render

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bernd/vibepit/tools/ccusage-go/internal/model"
)

// Table writes a simple tabular session report.
func Table(w io.Writer, rows []model.SessionRow, verbose bool) {
	var totals model.TokenUsage
	var totalCost float64
	if verbose {
		headers := []string{"PROVIDER", "DATE", "DIR", "SESSION", "MODELS", "INPUT", "OUTPUT", "CACHE", "TOTAL TOKENS", "COST_USD"}
		outRows := make([][]string, 0, len(rows)+1)
		for _, r := range rows {
			outRows = append(outRows, []string{
				string(r.Provider),
				r.LastActivity,
				compactProject(r.ProjectOrDir, true),
				compactSessionID(r.SessionID, true),
				strings.Join(cleanModels(r.Models, true), ","),
				formatThousands(r.Tokens.InputTokens),
				formatThousands(r.Tokens.OutputTokens),
				formatThousands(r.Tokens.CacheReadTokens),
				formatThousands(r.Tokens.TotalTokens),
				formatCurrency(r.CostUSD),
			})
			totals.InputTokens += r.Tokens.InputTokens
			totals.OutputTokens += r.Tokens.OutputTokens
			totals.CacheReadTokens += r.Tokens.CacheReadTokens
			totals.CacheCreateTokens += r.Tokens.CacheCreateTokens
			totals.ReasoningTokens += r.Tokens.ReasoningTokens
			totals.TotalTokens += r.Tokens.TotalTokens
			totalCost += r.CostUSD
		}
		outRows = append(outRows, []string{
			"TOTAL", "", "", "", "",
			formatThousands(totals.InputTokens),
			formatThousands(totals.OutputTokens),
			formatThousands(totals.CacheReadTokens),
			formatThousands(totals.TotalTokens),
			formatCurrency(totalCost),
		})
		renderAlignedTable(w, headers, outRows, map[int]bool{
			5: true,
			6: true,
			7: true,
			8: true,
			9: true,
		})
		return
	}

	headers := []string{"PROVIDER", "DATE", "MODELS", "INPUT", "OUTPUT", "CACHE", "TOTAL TOKENS", "COST_USD"}
	outRows := make([][]string, 0, len(rows)+1)
	aggregated := aggregateCompactRows(rows)
	for _, r := range aggregated {
		outRows = append(outRows, []string{
			string(r.Provider),
			r.LastActivity,
			strings.Join(cleanModels(r.Models, false), ","),
			formatThousands(r.Tokens.InputTokens),
			formatThousands(r.Tokens.OutputTokens),
			formatThousands(r.Tokens.CacheReadTokens),
			formatThousands(r.Tokens.TotalTokens),
			formatCurrency(r.CostUSD),
		})
		totals.InputTokens += r.Tokens.InputTokens
		totals.OutputTokens += r.Tokens.OutputTokens
		totals.CacheReadTokens += r.Tokens.CacheReadTokens
		totals.CacheCreateTokens += r.Tokens.CacheCreateTokens
		totals.ReasoningTokens += r.Tokens.ReasoningTokens
		totals.TotalTokens += r.Tokens.TotalTokens
		totalCost += r.CostUSD
	}

	outRows = append(outRows, []string{
		"TOTAL", "", "",
		formatThousands(totals.InputTokens),
		formatThousands(totals.OutputTokens),
		formatThousands(totals.CacheReadTokens),
		formatThousands(totals.TotalTokens),
		formatCurrency(totalCost),
	})

	renderAlignedTable(w, headers, outRows, map[int]bool{
		3: true,
		4: true,
		5: true,
		6: true,
		7: true,
	})
}

func aggregateCompactRows(rows []model.SessionRow) []model.SessionRow {
	type key struct {
		provider model.Provider
		date     string
	}
	grouped := map[key]*model.SessionRow{}
	for _, row := range rows {
		k := key{provider: row.Provider, date: row.LastActivity}
		existing := grouped[k]
		if existing == nil {
			existing = &model.SessionRow{
				Provider:     row.Provider,
				LastActivity: row.LastActivity,
				ModelUsage:   map[string]model.ModelUsage{},
			}
			grouped[k] = existing
		}
		existing.Tokens.InputTokens += row.Tokens.InputTokens
		existing.Tokens.OutputTokens += row.Tokens.OutputTokens
		existing.Tokens.CacheReadTokens += row.Tokens.CacheReadTokens
		existing.Tokens.CacheCreateTokens += row.Tokens.CacheCreateTokens
		existing.Tokens.ReasoningTokens += row.Tokens.ReasoningTokens
		existing.Tokens.TotalTokens += row.Tokens.TotalTokens
		existing.CostUSD += row.CostUSD
		existing.Models = append(existing.Models, row.Models...)
	}

	out := make([]model.SessionRow, 0, len(grouped))
	for _, row := range grouped {
		row.Models = uniqueSorted(row.Models)
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider == out[j].Provider {
			return out[i].LastActivity < out[j].LastActivity
		}
		return out[i].Provider < out[j].Provider
	})
	return out
}

func uniqueSorted(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func emptyDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}

func cleanModels(models []string, verbose bool) []string {
	if verbose {
		out := make([]string, 0, len(models))
		for _, m := range models {
			if strings.TrimSpace(m) != "" {
				out = append(out, m)
			}
		}
		if len(out) == 0 {
			return []string{"-"}
		}
		return out
	}

	out := make([]string, 0, len(models))
	for _, m := range models {
		m = strings.TrimSpace(m)
		if m == "" || m == "<unknown>" || m == "<synthetic>" {
			continue
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return []string{"-"}
	}
	if len(out) > 2 {
		return []string{out[0], out[1], fmt.Sprintf("+%d", len(out)-2)}
	}
	return out
}

func compactProject(project string, verbose bool) string {
	if verbose {
		return emptyDash(project)
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return "-"
	}
	parts := strings.Split(project, "/")
	last := parts[len(parts)-1]
	if strings.TrimSpace(last) == "" {
		last = project
	}
	return compactTail(last, 20)
}

func compactSessionID(sessionID string, verbose bool) string {
	if verbose {
		return sessionID
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "-"
	}
	parts := strings.Split(sessionID, "/")
	base := parts[len(parts)-1]
	if strings.TrimSpace(base) == "" {
		base = sessionID
	}
	if len(base) > 12 {
		return "…" + base[len(base)-11:]
	}
	return base
}

func compactTail(v string, max int) string {
	if max <= 0 || len(v) <= max {
		return v
	}
	return "…" + v[len(v)-max+1:]
}

func formatThousands(v int64) string {
	s := strconv.FormatInt(v, 10)
	if len(s) <= 3 {
		return s
	}
	negative := strings.HasPrefix(s, "-")
	if negative {
		s = s[1:]
	}

	firstGroup := len(s) % 3
	if firstGroup == 0 {
		firstGroup = 3
	}

	var b strings.Builder
	if negative {
		b.WriteByte('-')
	}
	b.WriteString(s[:firstGroup])
	for i := firstGroup; i < len(s); i += 3 {
		b.WriteByte('.')
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

func formatCurrency(v float64) string {
	return "$" + fmt.Sprintf("%.2f", v)
}

func renderAlignedTable(w io.Writer, headers []string, rows [][]string, rightAligned map[int]bool) {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = stringWidth(h)
	}

	for _, row := range rows {
		for i := range headers {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			if w := stringWidth(cell); w > widths[i] {
				widths[i] = w
			}
		}
	}

	writeRow := func(cols []string) {
		for i := range headers {
			cell := ""
			if i < len(cols) {
				cell = cols[i]
			}
			if rightAligned[i] {
				fmt.Fprintf(w, "%*s", widths[i], cell)
			} else {
				fmt.Fprintf(w, "%-*s", widths[i], cell)
			}
			if i < len(headers)-1 {
				fmt.Fprint(w, "  ")
			}
		}
		fmt.Fprintln(w)
	}

	writeRow(headers)
	for _, row := range rows {
		writeRow(row)
	}
}

func stringWidth(v string) int {
	return utf8.RuneCountInString(v)
}
