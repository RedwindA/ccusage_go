// Package config reads ccusage.json using the upstream configuration hierarchy.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/RedwindA/ccusage_go/internal/pricing"
)

type Store struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
type Config struct {
	Path       string
	Values     map[string]any
	PiStores   []Store
	storeError error
}

func Load(path string) (Config, error) {
	paths := []string{path}
	if path == "" {
		paths = []string{filepath.Join(".ccusage", "ccusage.json")}
		if env, present := os.LookupEnv("CLAUDE_CONFIG_DIR"); present {
			for _, dir := range strings.Split(env, ",") {
				if dir = strings.TrimSpace(dir); dir != "" {
					paths = append(paths, filepath.Join(dir, "ccusage.json"))
				}
			}
		} else if home, err := os.UserHomeDir(); err == nil {
			paths = append(paths, filepath.Join(home, ".config", "claude", "ccusage.json"), filepath.Join(home, ".claude", "ccusage.json"))
		}
	}
	for _, candidate := range paths {
		data, err := os.ReadFile(candidate)
		if err != nil {
			if path == "" {
				continue
			}
			return Config{}, err
		}
		c := Config{Path: candidate}
		if err = json.Unmarshal(data, &c.Values); err != nil {
			if path == "" {
				continue
			}
			return Config{}, fmt.Errorf("config %s: %w", candidate, err)
		}
		if c.Values == nil {
			if path == "" {
				continue
			}
			return Config{}, fmt.Errorf("config %s must be an object", candidate)
		}
		pi := object(c.Values["pi"])
		if stores, ok := pi["stores"]; ok {
			if _, valid := stores.([]any); !valid {
				c.storeError = fmt.Errorf("pi.stores must be an array")
				return c, nil
			}
			raw, _ := json.Marshal(stores)
			if err = json.Unmarshal(raw, &c.PiStores); err != nil {
				c.storeError = fmt.Errorf("pi.stores must be an array of name/path objects")
				return c, nil
			}
			seen := map[string]bool{}
			valid := regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
			reserved := ",all,claude,codex,amp,opencode,pi,openclaw,droid,codebuff,hermes,goose,kilo,kimi,qwen,copilot,gemini,antigravity,grok,zcode,"
			for _, store := range c.PiStores {
				if !valid.MatchString(store.Name) || strings.Contains(reserved, ","+store.Name+",") || seen[store.Name] || strings.TrimSpace(store.Path) == "" {
					c.storeError = fmt.Errorf("invalid or duplicate pi store %q", store.Name)
					return c, nil
				}
				seen[store.Name] = true
			}
		}
		return c, nil
	}
	return Config{Values: map[string]any{}}, nil
}
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func deepMerge(dst, src map[string]any) {
	for k, v := range src {
		if incoming, ok := v.(map[string]any); ok {
			child := object(dst[k])
			if child == nil {
				child = map[string]any{}
			}
			deepMerge(child, incoming)
			dst[k] = child
		} else {
			dst[k] = v
		}
	}
}

func merge(dst, src map[string]any) {
	for key, value := range src {
		if child, ok := value.(map[string]any); ok {
			copy := map[string]any{}
			if key == "pricingOverrides" {
				deepMerge(copy, object(dst[key]))
			}
			deepMerge(copy, child)
			dst[key] = copy
		} else {
			dst[key] = value
		}
	}
}

// Options returns an independent merged map; callers apply explicit CLI flags last.
func (c Config) Options(command, agent string) map[string]any {
	out := map[string]any{}
	merge(out, object(c.Values["defaults"]))
	commands := object(c.Values["commands"])
	raw := command
	if agent != "" {
		raw = agent + " " + command
	}
	merge(out, object(commands[raw]))
	if agent != "" {
		merge(out, object(commands[command]))
		merge(out, object(commands[agent+":"+command]))
		scope := object(c.Values[agent])
		merge(out, object(scope["defaults"]))
		merge(out, object(object(scope["commands"])[command]))
	}
	return out
}
func (c Config) PricingOverrides(command, agent string) map[string]pricing.Override {
	out := map[string]pricing.Override{}
	raw, _ := json.Marshal(c.Options(command, agent)["pricingOverrides"])
	_ = json.Unmarshal(raw, &out)
	return out
}
func validate(m map[string]any) error {
	for k, v := range m {
		if k == "pricingOverrides" {
			models, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("pricingOverrides must be an object")
			}
			for name, value := range models {
				fields, ok := value.(map[string]any)
				if !ok {
					return fmt.Errorf("pricing override %q must be an object", name)
				}
				for field, rate := range fields {
					n, ok := rate.(float64)
					if !ok || n < 0 {
						return fmt.Errorf("pricing override %q field %s must be a nonnegative number", name, field)
					}
				}
			}
			continue
		}
		if k == "since" || k == "until" {
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("%s must be a date string", k)
			}
			layout := "2006-01-02"
			if len(s) == 8 {
				layout = "20060102"
			}
			if _, err := time.Parse(layout, s); err != nil {
				return fmt.Errorf("%s %q must be a valid YYYY-MM-DD or YYYYMMDD date", k, s)
			}
		}
		if k == "mode" {
			s, ok := v.(string)
			if !ok || (s != "auto" && s != "calculate" && s != "display") {
				return fmt.Errorf("mode must be auto, calculate or display")
			}
		}
	}
	return nil
}

// Validate applies command-scoped checks after configuration discovery. A bad
// option for an unrelated command must not prevent this command from running.
func (c Config) Validate(command, agent string) error {
	if err := validate(c.Options(command, agent)); err != nil {
		return fmt.Errorf("config %s: %w", c.Path, err)
	}
	if agent == "" && (command == "daily" || command == "weekly" || command == "monthly" || command == "session") {
		return c.storeError
	}
	return nil
}
