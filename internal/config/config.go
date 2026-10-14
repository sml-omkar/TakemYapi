// Package config holds runtime configuration for the fetcher, sourced from
// command-line flags and environment variables.
package config

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
)

// Config contains everything needed to run a fetch run.
type Config struct {
	Domain       string
	APIKey       string
	OutputDir    string
	PerPage      int
	Concurrency  int
	UpdatedSince string
	Status       string
	SplitJSON    bool
	Attachments  bool
	Limit        int
	Ticket       int64
}

// Parse builds a Config from a .env file, environment variables, and
// command-line flags. Precedence: flag > environment variable > .env file.
func Parse(args []string) (*Config, error) {
	loadDotEnv(".env")

	fs := flag.NewFlagSet("fetcher", flag.ContinueOnError)

	domain := fs.String("domain", envOr("FRESHSERVICE_DOMAIN", ""), "Freshservice account slug (e.g. yourcompany) or full portal hostname (e.g. servicedesk.example.com)")
	apiKey := fs.String("api-key", envOr("FRESHSERVICE_API_KEY", ""), "Freshservice API key")
	outputDir := fs.String("out", envOr("FETCHER_OUT_DIR", "./output"), "Directory where JSON/CSV files are written")
	perPage := fs.Int("per-page", 100, "Number of tickets per page (max 100)")
	workers := fs.Int("concurrency", 5, "Number of concurrent conversation fetchers")
	updatedSince := fs.String("updated-since", "", "Only fetch tickets updated at or after this RFC3339 timestamp")
	status := fs.String("status", "", "Only fetch tickets with this status (2=Open, 3=Pending, 4=Resolved, 5=Closed, 6=Escalated)")
	splitJSON := fs.Bool("split-json", true, "Also write one JSON file per ticket")
	attachments := fs.Bool("attachments", true, "Download attachment binaries into output/attachments")
	limit := fs.Int("limit", 0, "Maximum number of tickets to fetch (0 = unlimited)")
	ticketID := fs.Int64("ticket", 0, "Fetch a single ticket by ID (ignores -limit)")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	cfg := &Config{
		Domain:       *domain,
		APIKey:       *apiKey,
		OutputDir:    *outputDir,
		PerPage:      *perPage,
		Concurrency:  *workers,
		UpdatedSince: *updatedSince,
		Status:       *status,
		SplitJSON:    *splitJSON,
		Attachments:  *attachments,
		Limit:        *limit,
		Ticket:       *ticketID,
	}

	if cfg.PerPage < 1 || cfg.PerPage > 100 {
		return nil, fmt.Errorf("per-page must be between 1 and 100, got %d", cfg.PerPage)
	}
	if cfg.Concurrency < 1 {
		return nil, fmt.Errorf("concurrency must be at least 1, got %d", cfg.Concurrency)
	}
	if cfg.Domain == "" || cfg.APIKey == "" {
		return nil, fmt.Errorf("both -domain and -api-key (or FRESHSERVICE_DOMAIN / FRESHSERVICE_API_KEY) are required")
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// loadDotEnv loads KEY=VALUE pairs from a .env file into the process
// environment, without overriding variables already set. Lines starting with
// # are ignored, and values may optionally be quoted.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		if _, exists := os.LookupEnv(k); !exists {
			os.Setenv(k, v)
		}
	}
}
