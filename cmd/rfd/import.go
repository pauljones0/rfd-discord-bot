package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/storage"
)

// runImport is deliberately independent of runtime credentials and networking.
func runImport(args []string) error {
	flags := flag.NewFlagSet("import", flag.ContinueOnError)
	file := flags.String("file", "", "versioned RFD migration JSON file")
	database := flags.String("database", "", "empty destination SQLite database")
	source := flags.String("source-app-id", "", "expected exporting Discord application ID")
	target := flags.String("target-app-id", "", "Discord application that will run this database")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *file == "" || *database == "" || *source == "" || *target == "" {
		return errors.New("usage: rfd-bot import --file export.json --database data/rfd.sqlite --source-app-id ID --target-app-id ID")
	}
	f, err := os.Open(*file)
	if err != nil {
		return fmt.Errorf("open migration file: %w", err)
	}
	defer f.Close()
	migration, err := storage.DecodeMigration(f)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	store, err := storage.Open(ctx, *database)
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := store.ImportMigration(ctx, migration, storage.ImportOptions{SourceApplicationID: *source, TargetApplicationID: *target})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
