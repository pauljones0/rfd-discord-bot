package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mitchellh/mapstructure"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

var errDocumentExists = errors.New("document already exists")

// Document is a raw JSONB-backed document row.
type Document struct {
	ID   string
	Data map[string]any
}

func (c *Client) usesPostgres() bool {
	return c != nil && c.pg != nil
}

func (c *Client) ensurePostgresSchema(ctx context.Context) error {
	_, err := c.pg.Exec(ctx, `
CREATE TABLE IF NOT EXISTS documents (
	collection text NOT NULL,
	doc_id text NOT NULL,
	data jsonb NOT NULL DEFAULT '{}'::jsonb,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (collection, doc_id)
);
CREATE INDEX IF NOT EXISTS documents_collection_updated_idx ON documents (collection, updated_at DESC);
CREATE INDEX IF NOT EXISTS documents_data_gin_idx ON documents USING gin (data jsonb_path_ops);
`)
	if err != nil {
		return fmt.Errorf("failed to initialize postgres document schema: %w", err)
	}
	return nil
}

// SetDocument upserts one JSONB document while preserving the supplied document ID.
func (c *Client) SetDocument(ctx context.Context, collection, docID string, value any) error {
	data, err := encodeDocument(value)
	if err != nil {
		return err
	}
	return c.SetRawDocument(ctx, collection, docID, data)
}

// CreateDocument creates one JSONB document and returns errDocumentExists if it already exists.
func (c *Client) CreateDocument(ctx context.Context, collection, docID string, value any) error {
	data, err := encodeDocument(value)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal document %s/%s: %w", collection, docID, err)
	}
	tag, err := c.pg.Exec(ctx, `
INSERT INTO documents (collection, doc_id, data)
VALUES ($1, $2, $3::jsonb)
ON CONFLICT DO NOTHING`, collection, docID, payload)
	if err != nil {
		return fmt.Errorf("create document %s/%s: %w", collection, docID, err)
	}
	if tag.RowsAffected() == 0 {
		return errDocumentExists
	}
	return nil
}

// SetRawDocument upserts one already-shaped Firestore-style document map.
func (c *Client) SetRawDocument(ctx context.Context, collection, docID string, data map[string]any) error {
	if collection == "" || docID == "" {
		return fmt.Errorf("collection and docID are required")
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal document %s/%s: %w", collection, docID, err)
	}
	_, err = c.pg.Exec(ctx, `
INSERT INTO documents (collection, doc_id, data)
VALUES ($1, $2, $3::jsonb)
ON CONFLICT (collection, doc_id)
DO UPDATE SET data = EXCLUDED.data, updated_at = now()`, collection, docID, payload)
	if err != nil {
		return fmt.Errorf("set document %s/%s: %w", collection, docID, err)
	}
	return nil
}

func (c *Client) AddDocument(ctx context.Context, collection string, value any) (string, error) {
	for i := 0; i < 5; i++ {
		docID := randomDocumentID()
		if err := c.CreateDocument(ctx, collection, docID, value); err == nil {
			return docID, nil
		} else if !errors.Is(err, errDocumentExists) {
			return "", err
		}
	}
	return "", fmt.Errorf("failed to allocate document ID for %s", collection)
}

func (c *Client) GetDocument(ctx context.Context, collection, docID string, dst any) (bool, error) {
	row, ok, err := c.GetRawDocument(ctx, collection, docID)
	if err != nil || !ok {
		return ok, err
	}
	if err := decodeDocument(row.Data, dst); err != nil {
		return false, fmt.Errorf("decode document %s/%s: %w", collection, docID, err)
	}
	return true, nil
}

func (c *Client) GetRawDocument(ctx context.Context, collection, docID string) (Document, bool, error) {
	var payload []byte
	err := c.pg.QueryRow(ctx, `SELECT data FROM documents WHERE collection=$1 AND doc_id=$2`, collection, docID).Scan(&payload)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Document{}, false, nil
		}
		return Document{}, false, fmt.Errorf("get document %s/%s: %w", collection, docID, err)
	}
	var data map[string]any
	if err := json.Unmarshal(payload, &data); err != nil {
		return Document{}, false, fmt.Errorf("unmarshal document %s/%s: %w", collection, docID, err)
	}
	return Document{ID: docID, Data: data}, true, nil
}

func (c *Client) DeleteDocument(ctx context.Context, collection, docID string) error {
	_, err := c.pg.Exec(ctx, `DELETE FROM documents WHERE collection=$1 AND doc_id=$2`, collection, docID)
	if err != nil {
		return fmt.Errorf("delete document %s/%s: %w", collection, docID, err)
	}
	return nil
}

func (c *Client) ListDocuments(ctx context.Context, collection string) ([]Document, error) {
	rows, err := c.pg.Query(ctx, `SELECT doc_id, data FROM documents WHERE collection=$1`, collection)
	if err != nil {
		return nil, fmt.Errorf("list documents %s: %w", collection, err)
	}
	defer rows.Close()

	var docs []Document
	for rows.Next() {
		var doc Document
		var payload []byte
		if err := rows.Scan(&doc.ID, &payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &doc.Data); err != nil {
			return nil, fmt.Errorf("unmarshal document %s/%s: %w", collection, doc.ID, err)
		}
		docs = append(docs, doc)
	}
	return docs, rows.Err()
}

func encodeDocument(value any) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	if m, ok := value.(map[string]any); ok {
		return cloneMap(m), nil
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return map[string]any{}, nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil, fmt.Errorf("document value must be a struct or map, got %T", value)
	}
	data, ok := encodeStruct(rv).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("failed to encode document %T", value)
	}
	return data, nil
}

func encodeStruct(rv reflect.Value) any {
	rt := rv.Type()
	out := make(map[string]any)
	for i := 0; i < rv.NumField(); i++ {
		field := rt.Field(i)
		if field.PkgPath != "" {
			continue
		}
		fv := rv.Field(i)
		name, omitEmpty, skip := firestoreFieldName(field)
		if skip {
			continue
		}
		if field.Anonymous && name == "" {
			if nested, ok := encodeValue(fv).(map[string]any); ok {
				for k, v := range nested {
					out[k] = v
				}
			}
			continue
		}
		if omitEmpty && isZeroValue(fv) {
			continue
		}
		out[name] = encodeValue(fv)
	}
	return out
}

func encodeValue(v reflect.Value) any {
	if !v.IsValid() {
		return nil
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		return encodeValue(v.Elem())
	}
	if v.Type() == reflect.TypeOf(time.Time{}) {
		return v.Interface()
	}
	switch v.Kind() {
	case reflect.Struct:
		return encodeStruct(v)
	case reflect.Slice, reflect.Array:
		out := make([]any, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			out = append(out, encodeValue(v.Index(i)))
		}
		return out
	case reflect.Map:
		out := make(map[string]any, v.Len())
		for _, key := range v.MapKeys() {
			out[fmt.Sprint(key.Interface())] = encodeValue(v.MapIndex(key))
		}
		return out
	default:
		return v.Interface()
	}
}

func firestoreFieldName(field reflect.StructField) (name string, omitEmpty bool, skip bool) {
	tag := field.Tag.Get("firestore")
	if tag == "-" {
		return "", false, true
	}
	parts := strings.Split(tag, ",")
	if len(parts) > 0 {
		name = parts[0]
	}
	for _, opt := range parts[1:] {
		if opt == "omitempty" {
			omitEmpty = true
		}
	}
	if name == "" && !field.Anonymous {
		name = lowerCamel(field.Name)
	}
	return name, omitEmpty, false
}

func lowerCamel(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func isZeroValue(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	return v.IsZero()
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func decodeDocument(data map[string]any, dst any) error {
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:           dst,
		TagName:          "firestore",
		WeaklyTypedInput: true,
		Squash:           true,
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			timeDecodeHook,
		),
	})
	if err != nil {
		return err
	}
	return decoder.Decode(data)
}

func timeDecodeHook(from reflect.Type, to reflect.Type, data any) (any, error) {
	if to != reflect.TypeOf(time.Time{}) {
		return data, nil
	}
	switch v := data.(type) {
	case time.Time:
		return v, nil
	case string:
		if strings.TrimSpace(v) == "" {
			return time.Time{}, nil
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05 -0700 MST"} {
			if parsed, err := time.Parse(layout, v); err == nil {
				return parsed, nil
			}
		}
		return time.Time{}, fmt.Errorf("parse time %q", v)
	default:
		return data, nil
	}
}

func documentString(data map[string]any, key string) string {
	if v, ok := data[key]; ok {
		return strings.TrimSpace(fmt.Sprint(v))
	}
	return ""
}

func documentBool(data map[string]any, key string) bool {
	switch v := data[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	default:
		return false
	}
}

func documentTime(data map[string]any, key string) time.Time {
	switch v := data[key].(type) {
	case time.Time:
		return v
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05 -0700 MST"} {
			if parsed, err := time.Parse(layout, v); err == nil {
				return parsed
			}
		}
	}
	return time.Time{}
}

func sortDocumentsByTime(docs []Document, key string, asc bool) {
	sort.Slice(docs, func(i, j int) bool {
		left := documentTime(docs[i].Data, key)
		right := documentTime(docs[j].Data, key)
		if asc {
			return left.Before(right)
		}
		return left.After(right)
	})
}

func sortDealsByPublished(deals []models.DealInfo) {
	sort.Slice(deals, func(i, j int) bool {
		return deals[i].PublishedTimestamp.After(deals[j].PublishedTimestamp)
	})
}
