package claude

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bernd/vibepit/tools/ccusage-go/internal/model"
)

const (
	claudeEnvConfigDir = "CLAUDE_CONFIG_DIR"
	projectsDirName    = "projects"
)

type usagePayload struct {
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	CacheReadTokens   int64 `json:"cache_read_input_tokens"`
	CacheCreateTokens int64 `json:"cache_creation_input_tokens"`
}

type messagePayload struct {
	ID    string       `json:"id"`
	Model string       `json:"model"`
	Usage usagePayload `json:"usage"`
}

type usageEntry struct {
	Timestamp string         `json:"timestamp"`
	RequestID string         `json:"requestId"`
	Message   messagePayload `json:"message"`
	CostUSD   *float64       `json:"costUSD"`
}

// ResolvePaths returns Claude roots that contain a projects subdirectory.
func ResolvePaths() ([]string, error) {
	var roots []string
	seen := map[string]struct{}{}

	if env := strings.TrimSpace(os.Getenv(claudeEnvConfigDir)); env != "" {
		for part := range strings.SplitSeq(env, ",") {
			p := strings.TrimSpace(part)
			if p == "" {
				continue
			}
			abs, err := filepath.Abs(p)
			if err != nil {
				continue
			}
			if !hasProjectsDir(abs) {
				continue
			}
			if _, ok := seen[abs]; ok {
				continue
			}
			seen[abs] = struct{}{}
			roots = append(roots, abs)
		}
		if len(roots) == 0 {
			return nil, errors.New("no valid Claude directories found in CLAUDE_CONFIG_DIR")
		}
		return roots, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	defaults := []string{
		filepath.Join(home, ".config", "claude"),
		filepath.Join(home, ".claude"),
	}
	for _, p := range defaults {
		if !hasProjectsDir(p) {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		roots = append(roots, p)
	}
	if len(roots) == 0 {
		return nil, errors.New("no valid Claude data directories found")
	}
	return roots, nil
}

func hasProjectsDir(root string) bool {
	st, err := os.Stat(filepath.Join(root, projectsDirName))
	return err == nil && st.IsDir()
}

// LoadSessionAggregates loads Claude usage grouped by project/session.
func LoadSessionAggregates(paths []string) ([]model.SessionAggregate, error) {
	roots := paths
	if len(roots) == 0 {
		resolved, err := ResolvePaths()
		if err != nil {
			return nil, err
		}
		roots = resolved
	}

	agg := map[string]*model.SessionAggregate{}
	processed := map[string]struct{}{}

	for _, root := range roots {
		projectBase := filepath.Join(root, projectsDirName)
		_ = filepath.WalkDir(projectBase, func(path string, d os.DirEntry, err error) error {
			if err != nil || d == nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(strings.ToLower(d.Name()), ".jsonl") {
				return nil
			}

			projectPath, sessionID, ok := extractSessionInfo(projectBase, path)
			if !ok {
				return nil
			}
			key := projectPath + "/" + sessionID
			session := agg[key]
			if session == nil {
				session = &model.SessionAggregate{
					Provider:     model.ProviderClaude,
					SessionID:    sessionID,
					ProjectOrDir: projectPath,
					Models:       map[string]*model.ModelUsage{},
				}
				agg[key] = session
			}

			if err := processFile(path, func(entry usageEntry) {
				if hash := dedupeHash(entry); hash != "" {
					if _, exists := processed[hash]; exists {
						return
					}
					processed[hash] = struct{}{}
				}

				date := dateKey(entry.Timestamp)
				if date > session.LastActivity {
					session.LastActivity = date
				}

				modelName := strings.TrimSpace(entry.Message.Model)
				if modelName == "" {
					modelName = "<unknown>"
				}
				mu := session.Models[modelName]
				if mu == nil {
					mu = &model.ModelUsage{Name: modelName}
					session.Models[modelName] = mu
				}

				mu.Usage.InputTokens += nonNegative(entry.Message.Usage.InputTokens)
				mu.Usage.OutputTokens += nonNegative(entry.Message.Usage.OutputTokens)
				mu.Usage.CacheReadTokens += nonNegative(entry.Message.Usage.CacheReadTokens)
				mu.Usage.CacheCreateTokens += nonNegative(entry.Message.Usage.CacheCreateTokens)
				mu.Usage.TotalTokens += nonNegative(entry.Message.Usage.InputTokens) +
					nonNegative(entry.Message.Usage.OutputTokens) +
					nonNegative(entry.Message.Usage.CacheReadTokens) +
					nonNegative(entry.Message.Usage.CacheCreateTokens)

				if entry.CostUSD != nil {
					session.ExplicitCostUSD += *entry.CostUSD
					mu.ExplicitCostUSD += *entry.CostUSD
					mu.HasExplicitCost = true
					if mu.ExplicitUsage == nil {
						mu.ExplicitUsage = &model.TokenUsage{}
					}
					mu.ExplicitUsage.InputTokens += nonNegative(entry.Message.Usage.InputTokens)
					mu.ExplicitUsage.OutputTokens += nonNegative(entry.Message.Usage.OutputTokens)
					mu.ExplicitUsage.CacheReadTokens += nonNegative(entry.Message.Usage.CacheReadTokens)
					mu.ExplicitUsage.CacheCreateTokens += nonNegative(entry.Message.Usage.CacheCreateTokens)
					mu.ExplicitUsage.TotalTokens += nonNegative(entry.Message.Usage.InputTokens) +
						nonNegative(entry.Message.Usage.OutputTokens) +
						nonNegative(entry.Message.Usage.CacheReadTokens) +
						nonNegative(entry.Message.Usage.CacheCreateTokens)
				}
			}); err != nil {
				return nil
			}

			return nil
		})
	}

	out := make([]model.SessionAggregate, 0, len(agg))
	for _, s := range agg {
		if s.LastActivity == "" {
			s.LastActivity = "1970-01-01"
		}
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastActivity < out[j].LastActivity })
	return out, nil
}

func processFile(path string, consume func(usageEntry)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	s.Buffer(buf, 1024*1024)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		var entry usageEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if strings.TrimSpace(entry.Timestamp) == "" {
			continue
		}
		consume(entry)
	}
	return s.Err()
}

func extractSessionInfo(projectBase, file string) (projectPath, sessionID string, ok bool) {
	rel, err := filepath.Rel(projectBase, file)
	if err != nil {
		return "", "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) < 2 {
		return "", "", false
	}
	sessionID = strings.TrimSpace(parts[len(parts)-2])
	if sessionID == "" {
		return "", "", false
	}
	projectPath = filepath.ToSlash(filepath.Join(parts[:len(parts)-2]...))
	if projectPath == "" || projectPath == "." {
		projectPath = "unknown"
	}
	return projectPath, sessionID, true
}

func dedupeHash(e usageEntry) string {
	msgID := strings.TrimSpace(e.Message.ID)
	reqID := strings.TrimSpace(e.RequestID)
	if msgID == "" || reqID == "" {
		return ""
	}
	return msgID + ":" + reqID
}

func nonNegative(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func dateKey(ts string) string {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.Local().Format("2006-01-02")
	}
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}
