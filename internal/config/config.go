package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Server   ServerConfig
	Upstream UpstreamConfig
	Logging  LoggingConfig
}

type ServerConfig struct {
	Listen string
}

type UpstreamConfig struct {
	BaseURL        string
	TimeoutSeconds int
	Timeout        time.Duration
}

type LoggingConfig struct {
	Dir              string
	PrettyJSON       bool
	ExpandNestedJSON bool
}

func Load(path string) (Config, error) {
	cfg := defaultConfig()

	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("config file %q not found; copy config.example.yaml to config.yaml first", path)
		}
		return Config{}, err
	}

	values, err := parseSimpleYAML(path)
	if err != nil {
		return Config{}, err
	}

	if value := values["server.listen"]; value != "" {
		cfg.Server.Listen = value
	}
	if value := values["upstream.base_url"]; value != "" {
		cfg.Upstream.BaseURL = strings.TrimRight(value, "/")
	}
	if value := values["upstream.timeout_seconds"]; value != "" {
		seconds, err := strconv.Atoi(value)
		if err != nil || seconds <= 0 {
			return Config{}, fmt.Errorf("upstream.timeout_seconds must be a positive integer")
		}
		cfg.Upstream.TimeoutSeconds = seconds
	}
	if value := values["logging.dir"]; value != "" {
		cfg.Logging.Dir = value
	}
	if value := values["logging.pretty_json"]; value != "" {
		pretty, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("logging.pretty_json must be true or false")
		}
		cfg.Logging.PrettyJSON = pretty
	}
	if value := values["logging.expand_nested_json"]; value != "" {
		expand, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("logging.expand_nested_json must be true or false")
		}
		cfg.Logging.ExpandNestedJSON = expand
	}
	if cfg.Upstream.BaseURL == "" {
		return Config{}, fmt.Errorf("upstream.base_url is required")
	}

	cfg.Upstream.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	return cfg, nil
}

func defaultConfig() Config {
	return Config{
		Server: ServerConfig{
			Listen: "127.0.0.1:8080",
		},
		Upstream: UpstreamConfig{
			BaseURL:        "",
			TimeoutSeconds: 120,
			Timeout:        120 * time.Second,
		},
		Logging: LoggingConfig{
			Dir:              "./logs",
			PrettyJSON:       true,
			ExpandNestedJSON: true,
		},
	}
}

func parseSimpleYAML(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	values := make(map[string]string)
	section := ""
	scanner := bufio.NewScanner(file)
	lineNumber := 0

	for scanner.Scan() {
		lineNumber++
		line := stripComment(scanner.Text())
		if strings.TrimSpace(line) == "" {
			continue
		}

		indent := leadingSpaces(line)
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, ":") {
			return nil, fmt.Errorf("invalid config line %d: %s", lineNumber, trimmed)
		}

		parts := strings.SplitN(trimmed, ":", 2)
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		if indent == 0 && value == "" {
			section = key
			continue
		}

		if section == "" {
			return nil, fmt.Errorf("config key %q must be inside a section", key)
		}

		values[section+"."+key] = unquote(value)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return values, nil
}

func stripComment(line string) string {
	inQuote := false
	quoteChar := rune(0)
	for index, char := range line {
		if char == '\'' || char == '"' {
			if !inQuote {
				inQuote = true
				quoteChar = char
			} else if quoteChar == char {
				inQuote = false
			}
		}
		if char == '#' && !inQuote {
			return line[:index]
		}
	}
	return line
}

func leadingSpaces(line string) int {
	count := 0
	for _, char := range line {
		if char != ' ' {
			break
		}
		count++
	}
	return count
}

func unquote(value string) string {
	if len(value) < 2 {
		return value
	}
	if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
		return value[1 : len(value)-1]
	}
	return value
}
