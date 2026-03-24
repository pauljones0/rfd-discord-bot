package storage

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const carfaxCacheCollection = "carfax_cache"
const carfaxOptionsCollection = "carfax_options"

// CarfaxCacheEntry stores a cached Carfax valuation result.
// These persist forever — a 2018 Honda Civic Sedan EX in Saskatchewan will always
// be worth roughly the same (modulo market shifts), and the exact Carfax strings
// (Make, Model, Trim, etc.) never change.
type CarfaxCacheEntry struct {
	Year         int       `firestore:"year"`
	Make         string    `firestore:"make"`
	Model        string    `firestore:"model"`
	Trim         string    `firestore:"trim"`
	PostalPrefix string    `firestore:"postal_prefix"` // first 3 chars of postal code
	LowValue     float64   `firestore:"low_value"`
	HighValue    float64   `firestore:"high_value"`
	MidValue     float64   `firestore:"mid_value"`
	CachedAt     time.Time `firestore:"cached_at"`
}

// CarfaxOptionsEntry stores the valid dropdown options returned by Carfax's cascade API.
// As we navigate Year → Make → Model → Trim → Engine etc., we cache every set of
// options we encounter. This means future lookups can skip the cascade API call
// entirely if we already know the valid strings for that combination.
//
// Examples of what gets cached:
//   - year=2018 → Makes: [Acura, Alfa Romeo, Audi, BMW, ...]
//   - year=2018, make=Honda → Models: [Accord Hybrid, Civic Sedan, CR-V, ...]
//   - year=2018, make=Honda, model=Civic Sedan → Trims: [DX, EX, EX-T, LX, ...]
//   - year=2018, make=Honda, model=Civic Sedan, trim=EX → Engines: [2.0L I4]
type CarfaxOptionsEntry struct {
	Property string    `firestore:"property"` // "Make", "Model", "Trim", "Engine", etc.
	Params   string    `firestore:"params"`   // normalized parent params, e.g. "year=2018&make=Honda"
	Options  []string  `firestore:"options"`  // the valid dropdown values
	CachedAt time.Time `firestore:"cached_at"`
}

// carfaxCacheKeyRe strips non-alphanumeric characters (except hyphens) for cache key generation.
var carfaxCacheKeyRe = regexp.MustCompile(`[^a-z0-9-]`)

// CarfaxNormalize lowercases, trims, replaces spaces with hyphens, and strips
// invalid characters from a string for use in Firestore document IDs.
func CarfaxNormalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "-")
	s = carfaxCacheKeyRe.ReplaceAllString(s, "")
	return s
}

// carfaxCacheKey builds a normalized Firestore document ID for a Carfax cache entry.
// Format: {year}_{make}_{model}_{trim}_{postal3}
func carfaxCacheKey(year int, make, model, trim, postalPrefix string) string {
	return fmt.Sprintf("%d_%s_%s_%s_%s",
		year,
		CarfaxNormalize(make),
		CarfaxNormalize(model),
		CarfaxNormalize(trim),
		CarfaxNormalize(postalPrefix),
	)
}

// carfaxOptionsKey builds a normalized Firestore document ID for cached dropdown options.
// Format: {property}_{normalized_params} e.g. "model_2018_honda" or "trim_2018_honda_civic-sedan"
func carfaxOptionsKey(property, normalizedParams string) string {
	return CarfaxNormalize(property) + "_" + normalizedParams
}

// GetCarfaxCache retrieves a cached Carfax valuation. Returns nil if not found.
// Valuations persist forever — no TTL expiry.
func (c *Client) GetCarfaxCache(ctx context.Context, year int, make, model, trim, postalPrefix string) (*CarfaxCacheEntry, error) {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	docID := carfaxCacheKey(year, make, model, trim, postalPrefix)
	doc, err := c.client.Collection(carfaxCacheCollection).Doc(docID).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get carfax cache entry: %w", err)
	}

	var entry CarfaxCacheEntry
	if err := doc.DataTo(&entry); err != nil {
		return nil, fmt.Errorf("failed to unmarshal carfax cache entry: %w", err)
	}

	return &entry, nil
}

// SaveCarfaxCache stores a Carfax valuation result in the cache (persists forever).
func (c *Client) SaveCarfaxCache(ctx context.Context, entry *CarfaxCacheEntry) error {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	postalPrefix := entry.PostalPrefix
	if len(postalPrefix) > 3 {
		postalPrefix = postalPrefix[:3]
	}

	docID := carfaxCacheKey(entry.Year, entry.Make, entry.Model, entry.Trim, postalPrefix)
	entry.CachedAt = time.Now()

	_, err := c.client.Collection(carfaxCacheCollection).Doc(docID).Set(ctx, entry)
	if err != nil {
		return fmt.Errorf("failed to save carfax cache entry: %w", err)
	}

	slog.Info("Cached Carfax valuation",
		"processor", "facebook",
		"doc_id", docID,
		"year", entry.Year,
		"make", entry.Make,
		"model", entry.Model,
		"trim", entry.Trim,
		"mid_value", entry.MidValue)
	return nil
}

// GetCarfaxOptions retrieves cached dropdown options for a given cascade step.
// Returns nil if not found. Options persist forever — valid makes/models/trims don't change.
//
// property: "Make", "Model", "Trim", "Engine", etc.
// normalizedParams: pre-normalized key fragment, e.g. "2018_honda" for Model lookups.
func (c *Client) GetCarfaxOptions(ctx context.Context, property, normalizedParams string) ([]string, error) {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	docID := carfaxOptionsKey(property, normalizedParams)
	doc, err := c.client.Collection(carfaxOptionsCollection).Doc(docID).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get carfax options: %w", err)
	}

	var entry CarfaxOptionsEntry
	if err := doc.DataTo(&entry); err != nil {
		return nil, fmt.Errorf("failed to unmarshal carfax options: %w", err)
	}

	return entry.Options, nil
}

// SaveCarfaxOptions caches the dropdown options returned by a cascade API call.
// This allows future lookups to skip the reCAPTCHA-protected API call entirely.
//
// property: "Make", "Model", "Trim", "Engine", etc.
// normalizedParams: pre-normalized key fragment, e.g. "2018_honda".
// options: the valid dropdown values returned by Carfax.
func (c *Client) SaveCarfaxOptions(ctx context.Context, property, normalizedParams string, options []string) error {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	docID := carfaxOptionsKey(property, normalizedParams)
	entry := CarfaxOptionsEntry{
		Property: property,
		Params:   normalizedParams,
		Options:  options,
		CachedAt: time.Now(),
	}

	_, err := c.client.Collection(carfaxOptionsCollection).Doc(docID).Set(ctx, entry)
	if err != nil {
		return fmt.Errorf("failed to save carfax options: %w", err)
	}

	slog.Debug("Cached Carfax dropdown options",
		"processor", "facebook",
		"property", property,
		"params", normalizedParams,
		"option_count", len(options))
	return nil
}
