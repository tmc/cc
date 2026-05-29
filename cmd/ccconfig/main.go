package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tmc/cc/ccgit"
	"github.com/tmc/cc/ccpaths"
)

var (
	listFlag       = flag.Bool("list", false, "List all configuration")
	lFlag          = flag.Bool("l", false, "List all configuration (short)")
	getFlag        = flag.Bool("get", false, "Get value for key")
	unsetFlag      = flag.Bool("unset", false, "Remove a key")
	localFlag      = flag.Bool("local", false, "Use local config")
	projectFlag    = flag.Bool("project", false, "Use project config")
	globalFlag     = flag.Bool("global", false, "Use global config")
	showOriginFlag = flag.Bool("show-origin", false, "Show config file source")
	editFlag       = flag.Bool("edit", false, "Open config file in editor")
	fileFlag       = flag.String("file", "", "Use specific config file")
	geminiFlag     = flag.Bool("gemini", false, "Use Gemini CLI paths instead of Claude Code")
)

// loadedConfig represents a loaded configuration.
type loadedConfig struct {
	values map[string]string
	file   string
	scope  string
}

func main() {
	flag.Parse()

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ccconfig: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Handle list flag
	if *listFlag || *lFlag {
		return listConfig()
	}

	// Handle edit flag
	if *editFlag {
		return editConfig()
	}

	// Get key from args
	args := flag.Args()
	if len(args) == 0 {
		return fmt.Errorf("no key specified (use --list to show all)")
	}

	key := args[0]

	// Handle unset
	if *unsetFlag {
		return unsetKey(key)
	}

	// Handle set (key + value provided)
	if len(args) >= 2 {
		value := strings.Join(args[1:], " ")
		return setKey(key, value)
	}

	// Handle get
	return getKey(key)
}

func listConfig() error {
	configs := loadAllConfigs()

	// Merge all configs (respecting precedence)
	merged := make(map[string]struct {
		value  string
		origin string
		scope  string
	})

	// Load in reverse precedence order (global, project, local)
	// so higher precedence overwrites lower
	for _, cfg := range configs {
		for k, v := range cfg.values {
			merged[k] = struct {
				value  string
				origin string
				scope  string
			}{v, cfg.file, cfg.scope}
		}
	}

	// Sort keys for consistent output
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		v := merged[k]
		if *showOriginFlag {
			fmt.Printf("%s\t%s=%s\n", v.origin, k, v.value)
		} else {
			fmt.Printf("%s=%s\n", k, v.value)
		}
	}

	return nil
}

func getKey(key string) error {
	configs := loadAllConfigs()

	// Check configs in precedence order (local, project, global)
	for i := len(configs) - 1; i >= 0; i-- {
		cfg := configs[i]
		if v, ok := cfg.values[key]; ok {
			if *showOriginFlag {
				fmt.Printf("%s\t%s\n", cfg.file, v)
			} else {
				fmt.Println(v)
			}
			return nil
		}
	}

	return fmt.Errorf("key not found: %s", key)
}

func setKey(key, value string) error {
	path, err := getConfigPath()
	if err != nil {
		return err
	}

	// Load existing config
	cfg := loadConfig(path, "")

	// Set the new value
	cfg.values[key] = value

	// Write back
	return writeConfig(path, cfg.values)
}

func unsetKey(key string) error {
	path, err := getConfigPath()
	if err != nil {
		return err
	}

	// Load existing config
	cfg := loadConfig(path, "")

	// Remove the key
	delete(cfg.values, key)

	// Write back
	return writeConfig(path, cfg.values)
}

func editConfig() error {
	path, err := getConfigPath()
	if err != nil {
		return err
	}

	// Ensure file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create config directory: %w", err)
		}
		if err := os.WriteFile(path, []byte{}, 0644); err != nil {
			return fmt.Errorf("create config file: %w", err)
		}
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	fmt.Printf("Opening %s with %s\n", path, editor)
	// Note: In a real implementation, we'd exec the editor
	// For now, just print the path
	fmt.Println(path)
	return nil
}

func getConfigPath() (string, error) {
	if *fileFlag != "" {
		return *fileFlag, nil
	}

	dirName := ".claude"
	if *geminiFlag {
		dirName = ".gemini"
	}

	if *localFlag {
		return filepath.Join(".", dirName, "config"), nil
	}

	if *projectFlag {
		gitCtx, err := ccgit.ResolveGitContext("")
		if err != nil {
			return "", fmt.Errorf("not in a git repository")
		}
		return filepath.Join(gitCtx.WorktreePath, dirName, "config"), nil
	}

	// Default to global
	var ch string
	if *geminiFlag {
		ch, _ = ccpaths.GeminiHome()
	} else {
		ch, _ = ccpaths.ClaudeHome()
	}
	return filepath.Join(ch, "config"), nil
}

func loadAllConfigs() []loadedConfig {
	var configs []loadedConfig

	var ch string
	if *geminiFlag {
		ch, _ = ccpaths.GeminiHome()
	} else {
		ch, _ = ccpaths.ClaudeHome()
	}

	dirName := ".claude"
	if *geminiFlag {
		dirName = ".gemini"
	}

	// Global config
	globalPath := filepath.Join(ch, "config")
	if cfg := loadConfig(globalPath, "global"); len(cfg.values) > 0 {
		configs = append(configs, cfg)
	}

	if gitCtx, err := ccgit.ResolveGitContext(""); err == nil {
		projectPath := filepath.Join(gitCtx.WorktreePath, dirName, "config")
		if cfg := loadConfig(projectPath, "project"); len(cfg.values) > 0 {
			configs = append(configs, cfg)
		}
	}

	// Local config
	localPath := filepath.Join(".", dirName, "config")
	if cfg := loadConfig(localPath, "local"); len(cfg.values) > 0 {
		configs = append(configs, cfg)
	}

	return configs
}

func loadConfig(path, scope string) loadedConfig {
	cfg := loadedConfig{
		values: make(map[string]string),
		file:   path,
		scope:  scope,
	}

	f, err := os.Open(path)
	if err != nil {
		return cfg
	}
	defer f.Close()

	section := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		// Section header
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[]")
			continue
		}

		// Key-value pair
		if idx := strings.IndexAny(line, "="); idx > 0 {
			key := strings.TrimSpace(line[:idx])
			value := strings.TrimSpace(line[idx+1:])
			// Remove quotes if present
			value = strings.Trim(value, "\"'")

			if section != "" {
				key = section + "." + key
			}
			cfg.values[key] = value
		}
	}

	return cfg
}

func writeConfig(path string, values map[string]string) error {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	// Group by section
	sections := make(map[string]map[string]string)
	for k, v := range values {
		parts := strings.SplitN(k, ".", 2)
		if len(parts) == 2 {
			if sections[parts[0]] == nil {
				sections[parts[0]] = make(map[string]string)
			}
			sections[parts[0]][parts[1]] = v
		} else {
			if sections[""] == nil {
				sections[""] = make(map[string]string)
			}
			sections[""][k] = v
		}
	}

	// Write file
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create config file: %w", err)
	}
	defer f.Close()

	// Sort section names
	sectionNames := make([]string, 0, len(sections))
	for name := range sections {
		sectionNames = append(sectionNames, name)
	}
	sort.Strings(sectionNames)

	for _, section := range sectionNames {
		keys := sections[section]

		if section != "" {
			fmt.Fprintf(f, "[%s]\n", section)
		}

		// Sort keys within section
		keyNames := make([]string, 0, len(keys))
		for k := range keys {
			keyNames = append(keyNames, k)
		}
		sort.Strings(keyNames)

		for _, key := range keyNames {
			value := keys[key]
			// Quote values with spaces
			if strings.Contains(value, " ") {
				value = fmt.Sprintf("%q", value)
			}
			fmt.Fprintf(f, "%s = %s\n", key, value)
		}
		fmt.Fprintln(f)
	}

	return nil
}
