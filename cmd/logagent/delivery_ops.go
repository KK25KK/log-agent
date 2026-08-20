package main

import (
	"context"
	"errors"
	"flag"
	"time"

	"logagent/internal/adapters/sqlite"
)

func runDeliveryDLQListCommand(args []string) error {
	flags := flag.NewFlagSet("delivery-dlq-list", flag.ContinueOnError)
	databasePath := flags.String("db", "./data/logagent.db", "SQLite database path")
	limit := flags.Int("limit", 50, "maximum dead deliveries to return")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: logagent delivery-dlq-list [--db path] [--limit 50]")
	}
	store, err := sqlite.Open(*databasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	items, err := store.ListDeadDeliveries(context.Background(), *limit)
	if err != nil {
		return err
	}
	return printJSON(items)
}

func runDeliveryDLQReplayCommand(args []string) error {
	flags := flag.NewFlagSet("delivery-dlq-replay", flag.ContinueOnError)
	databasePath := flags.String("db", "./data/logagent.db", "SQLite database path")
	deliveryID := flags.String("delivery-id", "", "dead delivery ID")
	operatorRef := flags.String("operator", "", "audited operator reference")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *deliveryID == "" || *operatorRef == "" {
		return errors.New("usage: logagent delivery-dlq-replay [--db path] --delivery-id id --operator ref")
	}
	store, err := sqlite.Open(*databasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := store.ReplayDeadDelivery(context.Background(), *deliveryID, *operatorRef, time.Now().UTC())
	if err != nil {
		return err
	}
	return printJSON(result)
}
