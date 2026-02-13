package codex

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bernd/vibepit/tools/ccusage-go/internal/model"
)

const (
	codexHomeEnv      = "CODEX_HOME"
	defaultFallback   = "gpt-5"
	sessionSubdirName = "sessions"
)

type rawUsage struct {
	InputTokens          int64 `json:"input_tokens"`
	CachedInputTokens    int64 `json:"cached_input_tokens"`
	CacheReadInputTokens int64 `json:"cache_read_input_tokens"`
	OutputTokens         int64 `json:"output_tokens"`
	ReasoningOutput      int64 `json:"reasoning_output_tokens"`
	TotalTokens          int64 `json:"total_tokens"`
}

type tokenCountInfo struct {
	Model           string         `json:"model"`
	ModelName       string         `json:"model_name"`
	Metadata        map[string]any `json:"metadata"`
	LastTokenUsage  *rawUsage      `json:"last_token_usage"`
	TotalTokenUsage *rawUsage      `json:"total_token_usage"`
}

type eventPayload struct {
	Type     string          `json:"type"`
	Info     *tokenCountInfo `json:"info"`
	Model    string          `json:"model"`
	Metadata map[string]any  `json:"metadata"`
}

type logEntry struct {
	Timestamp string       `json:"timestamp"`
	Type      string       `json:"type"`
	Payload   eventPayload `json:"payload"`
}

// LoadResult includes parsed sessions and missing directories.
type LoadResult struct {
	Sessions           []model.SessionAggregate `json:"sessions"`
	MissingDirectories []string                 `json:"missingDirectories"`
}

// ResolveSessionDirs returns Codex session roots.
func ResolveSessionDirs() []string {
	if env := strings.TrimSpace(os.Getenv(codexHomeEnv)); env != "" {
		return []string{filepath.Join(env, sessionSubdirName)}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return []string{filepath.Join("~", ".codex", sessionSubdirName)}
	}
	return []string{filepath.Join(home, ".codex", sessionSubdirName)}
}

// LoadSessionAggregates loads Codex usage grouped by session file.
func LoadSessionAggregates(sessionDirs []string) (LoadResult, error) {
	dirs := sessionDirs
	if len(dirs) == 0 {
		dirs = ResolveSessionDirs()
	}

	result := LoadResult{}
	agg := map[string]*model.SessionAggregate{}

	for _, dir := range dirs {
		st, err := os.Stat(dir)
		if err != nil || !st.IsDir() {
			result.MissingDirectories = append(result.MissingDirectories, dir)
			continue
		}
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d == nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(strings.ToLower(d.Name()), ".jsonl") {
				return nil
			}
			rel, relErr := filepath.Rel(dir, path)
			if relErr != nil {
				return nil
			}
			sessionID := strings.TrimSuffix(filepath.ToSlash(rel), ".jsonl")
			if strings.TrimSpace(sessionID) == "" {
				return nil
			}
			dirPart := filepath.ToSlash(filepath.Dir(sessionID))
			if dirPart == "." {
				dirPart = ""
			}

			key := dir + "::" + sessionID
			s := agg[key]
			if s == nil {
				s = &model.SessionAggregate{
					Provider:     model.ProviderCodex,
					SessionID:    sessionID,
					ProjectOrDir: dirPart,
					Models:       map[string]*model.ModelUsage{},
				}
				agg[key] = s
			}
			if err := processCodexFile(path, s); err != nil {
				return nil
			}
			return nil
		})
	}

	for _, s := range agg {
		if len(s.Models) == 0 {
			continue
		}
		if s.LastActivity == "" {
			s.LastActivity = "1970-01-01"
		}
		result.Sessions = append(result.Sessions, *s)
	}
	sort.Slice(result.Sessions, func(i, j int) bool {
		return result.Sessions[i].LastActivity < result.Sessions[j].LastActivity
	})
	return result, nil
}

func processCodexFile(path string, s *model.SessionAggregate) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var currentModel string
	var currentModelFallback bool
	var previousTotals *rawUsage

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e logEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}

		if e.Type == "turn_context" {
			if m := extractModel(e); m != "" {
				currentModel = m
				currentModelFallback = false
			}
			continue
		}
		if e.Type != "event_msg" || e.Payload.Type != "token_count" || e.Payload.Info == nil {
			continue
		}
		if strings.TrimSpace(e.Timestamp) == "" {
			continue
		}

		modelName := extractModel(e)
		isFallback := false
		if modelName == "" {
			modelName = currentModel
		}
		if modelName == "" {
			modelName = defaultFallback
			isFallback = true
			currentModel = modelName
			currentModelFallback = true
		} else if modelName == currentModel && currentModelFallback {
			isFallback = true
		}

		delta := rawUsage{}
		if e.Payload.Info.LastTokenUsage != nil {
			delta = *e.Payload.Info.LastTokenUsage
		} else if e.Payload.Info.TotalTokenUsage != nil {
			t := e.Payload.Info.TotalTokenUsage
			delta = subtractUsage(*t, previousTotals)
		}

		if e.Payload.Info.TotalTokenUsage != nil {
			copyTotal := *e.Payload.Info.TotalTokenUsage
			previousTotals = &copyTotal
		}

		if delta.InputTokens == 0 && delta.OutputTokens == 0 && delta.CachedInputTokens == 0 && delta.CacheReadInputTokens == 0 && delta.ReasoningOutput == 0 && delta.TotalTokens == 0 {
			continue
		}

		cacheRead := delta.CachedInputTokens
		if cacheRead == 0 {
			cacheRead = delta.CacheReadInputTokens
		}
		if cacheRead > delta.InputTokens {
			cacheRead = delta.InputTokens
		}
		total := delta.TotalTokens
		if total <= 0 {
			total = delta.InputTokens + delta.OutputTokens
		}

		mu := s.Models[modelName]
		if mu == nil {
			mu = &model.ModelUsage{Name: modelName}
			s.Models[modelName] = mu
		}
		mu.Usage.InputTokens += nonNegative(delta.InputTokens)
		mu.Usage.OutputTokens += nonNegative(delta.OutputTokens)
		mu.Usage.CacheReadTokens += nonNegative(cacheRead)
		mu.Usage.ReasoningTokens += nonNegative(delta.ReasoningOutput)
		mu.Usage.TotalTokens += nonNegative(total)
		if isFallback {
			mu.IsFallback = true
		}

		date := dateKey(e.Timestamp)
		if date > s.LastActivity {
			s.LastActivity = date
		}
	}

	return sc.Err()
}

func extractModel(e logEntry) string {
	if e.Payload.Info != nil {
		if m := strings.TrimSpace(e.Payload.Info.Model); m != "" {
			return m
		}
		if m := strings.TrimSpace(e.Payload.Info.ModelName); m != "" {
			return m
		}
		if m := modelFromMetadata(e.Payload.Info.Metadata); m != "" {
			return m
		}
	}
	if m := strings.TrimSpace(e.Payload.Model); m != "" {
		return m
	}
	if m := modelFromMetadata(e.Payload.Metadata); m != "" {
		return m
	}
	return ""
}

func subtractUsage(current rawUsage, previous *rawUsage) rawUsage {
	if previous == nil {
		return current
	}
	return rawUsage{
		InputTokens:          max(current.InputTokens-(previous.InputTokens), 0),
		CachedInputTokens:    max(current.CachedInputTokens-(previous.CachedInputTokens), 0),
		CacheReadInputTokens: max(current.CacheReadInputTokens-(previous.CacheReadInputTokens), 0),
		OutputTokens:         max(current.OutputTokens-(previous.OutputTokens), 0),
		ReasoningOutput:      max(current.ReasoningOutput-(previous.ReasoningOutput), 0),
		TotalTokens:          max(current.TotalTokens-(previous.TotalTokens), 0),
	}
}

func modelFromMetadata(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	raw, ok := metadata["model"]
	if !ok {
		return ""
	}
	model, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(model)
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
