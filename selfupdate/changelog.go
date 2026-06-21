package selfupdate

import (
	"fmt"
	"strings"
)

// repoURL is the GitHub repository used to build changelog PR/issue links.
// Must match REPO_URL in .github/scripts/generate-release-metadata.py.
const repoURL = "https://github.com/bernd/vibepit"

// changelogCategories is the canonical render order for changelog sections.
// Must match CHANGELOG_CATEGORIES in the metadata generator.
var changelogCategories = []string{"added", "changed", "fixed", "deprecated", "removed", "security"}

// refSuffix renders the trailing " ([#pr](...), [#issue](...))" for an entry,
// or "" when it has no references.
func refSuffix(e ChangelogEntry) string {
	var refs []string
	if e.PR != "" {
		refs = append(refs, fmt.Sprintf("[#%s](%s/pull/%s)", e.PR, repoURL, e.PR))
	}
	if e.Issue != "" {
		refs = append(refs, fmt.Sprintf("[#%s](%s/issues/%s)", e.Issue, repoURL, e.Issue))
	}
	if len(refs) == 0 {
		return ""
	}
	return " (" + strings.Join(refs, ", ") + ")"
}

// formatEntry renders a single entry line without a version prefix.
func formatEntry(e ChangelogEntry) string {
	return "- " + e.Msg + refSuffix(e)
}

// capitalizeCategory upper-cases the first letter of a category name to match
// Python's str.capitalize() for these single lowercase words.
func capitalizeCategory(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// renderSections renders the per-category blocks shared by RenderChanges and
// RenderMerged: a "\nCategory:" header per non-empty category in canonical
// order, then one line per entry as produced by line. Categories are visited
// in changelogCategories order so output matches the Python generator.
func renderSections[E any](sections map[string][]E, line func(E) string) string {
	var lines []string
	for _, cat := range changelogCategories {
		entries := sections[cat]
		if len(entries) == 0 {
			continue
		}
		lines = append(lines, "\n"+capitalizeCategory(cat)+":")
		for _, e := range entries {
			lines = append(lines, line(e))
		}
	}
	return strings.Join(lines, "\n")
}

// RenderChanges renders a single version's structured changes to the same text
// the Python generator produces for the rendered "changelog" field. No version
// prefix is added.
func RenderChanges(changes map[string][]ChangelogEntry) string {
	return renderSections(changes, formatEntry)
}

// MergeChanges concatenates the structured changes of multiple releases by
// category, tagging each entry with its source version. metas must be ordered
// newest-first so that the newest release's entries lead each category.
func MergeChanges(metas []*VersionMetadata) map[string][]MergedEntry {
	merged := make(map[string][]MergedEntry)
	for _, m := range metas {
		for cat, entries := range m.Changes {
			for _, e := range entries {
				merged[cat] = append(merged[cat], MergedEntry{Entry: e, Version: m.Version})
			}
		}
	}
	return merged
}

// RenderMerged renders merged changes as a single block, prefixing every entry
// with its source version (e.g. "- v0.4.0: ...").
func RenderMerged(merged map[string][]MergedEntry) string {
	return renderSections(merged, func(m MergedEntry) string {
		return "- v" + m.Version + ": " + m.Entry.Msg + refSuffix(m.Entry)
	})
}
