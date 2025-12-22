# Project Cleanup Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Centralize configuration, remove hardcoded values, and improve repository hygiene in the `rfd-discord-bot` project.

**Architecture:** We will transform `internal/config` into a central configuration hub using a singleton pattern or a dependency-injected struct. Components will be refactored to consume this configuration, allowing for the removal of hardcoded affiliate tags, domain lists, and duplicated constants.

**Tech Stack:** Go 1.23.0, Firestore, Discord Webhooks.

---

### Task 1: Centralize Configuration Struct

**Files:**
- Modify: `internal/config/config.go`

**Step 1: Write the failing test**

```go
package config

import (
	"os"
	"testing"
)

func test_ConfigLoading(t *testing.T) {
	os.Setenv("DISCORD_WEBHOOK_URL", "https://test.webhook")
	os.Setenv("AMAZON_AFFILIATE_TAG", "test-tag-20")

	cfg := Load()
	if cfg.Discord.WebhookURL != "https://test.webhook" {
		t.Errorf("Expected https://test.webhook, got %s", cfg.Discord.WebhookURL)
	}
	if cfg.Referral.AmazonAffiliateTag != "test-tag-20" {
		t.Errorf("Expected test-tag-20, got %s", cfg.Referral.AmazonAffiliateTag)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/config/...`
Expected: FAIL (struct fields or Load function don't exist yet)

**Step 3: Write minimal implementation**

```go
package config

import "os"

type Config struct {
	Discord struct {
		WebhookURL     string
		UpdateInterval string
	}
	Referral struct {
		AmazonAffiliateTag string
	}
	Scraper struct {
		AllowedDomains []string
	}
}

func Load() *Config {
	return &Config{
		Discord: struct {
			WebhookURL     string
			UpdateInterval string
		}{
			WebhookURL:     os.Getenv("DISCORD_WEBHOOK_URL"),
			UpdateInterval: "10m",
		},
		Referral: struct {
			AmazonAffiliateTag string
		}{
			AmazonAffiliateTag: os.Getenv("AMAZON_AFFILIATE_TAG"),
		},
	}
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/config/...`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/config/config.go
git commit -m "refactor: centralize configuration struct"
```

---

### Task 2: Refactor Referral Cleaning

**Files:**
- Modify: `internal/util/referral.go`

**Step 1: Write the failing test**

```go
func test_CleanReferralWithConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.Referral.AmazonAffiliateTag = "new-tag-20"
	url := "https://amazon.ca/dp/ASIN?tag=old-tag-20"
	cleaned := CleanURL(url, cfg)
	if !strings.Contains(cleaned, "tag=new-tag-20") {
		t.Errorf("Expected new tag, got %s", cleaned)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/util/...`
Expected: FAIL (CleanURL doesn't take config yet)

**Step 3: Write minimal implementation**

Update `CleanURL` in `internal/util/referral.go` to accept `*config.Config` and use `cfg.Referral.AmazonAffiliateTag`.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/util/...`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/util/referral.go
git commit -m "refactor: use config for affiliate tags"
```

---

### Task 3: Repository Hygiene

**Files:**
- Delete: `server`
- Move: `example.png` -> `docs/assets/example.png`
- Modify: `.gitignore`

**Step 1: Delete the binary**

Run: `rm server`

**Step 2: Relocate the asset**

Run: `mkdir -p docs/assets && mv example.png docs/assets/`

**Step 3: Update .gitignore**

```text
/server
/bin/
*.exe
```

**Step 4: Commit**

```bash
git add .gitignore docs/assets/
git rm server
git commit -m "chore: clean up repository artifacts"
```
