// collie-feishu-pm processes one private queue item for recommendation only.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/lihuanyu0w0/collie/internal/feishuobserve"
)

func run(ctx context.Context) error {
	dbPath := flag.String("db", "", "absolute private observer SQLite path")
	endpoint := flag.String("endpoint", "", "approved full HTTPS completion URL")
	model := flag.String("model", "", "configured provider model")
	approve := flag.Bool("approve-provider-data", false, "explicitly authorize sending queued group names and text to this approved provider")
	flag.Parse()
	if !*approve || *dbPath == "" || flag.NArg() != 0 {
		return errors.New("provide --db, --endpoint, --model and --approve-provider-data; set COLLIE_FEISHU_PM_TOKEN")
	}
	reviewer, err := feishuobserve.NewPMReviewer(*endpoint, *model, os.Getenv("COLLIE_FEISHU_PM_TOKEN"))
	if err != nil {
		return err
	}
	db, err := feishuobserve.Open(*dbPath)
	if err != nil {
		return errors.New("cannot open private observer database")
	}
	defer db.Close()
	status, err := reviewer.ReviewNext(ctx, db)
	if err != nil {
		return err
	}
	fmt.Println(status)
	return nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
