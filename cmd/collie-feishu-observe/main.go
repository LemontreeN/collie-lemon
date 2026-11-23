// collie-feishu-observe stages Feishu events without starting or communicating with a Collie hub.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/lihuanyu0w0/collie/internal/feishuobserve"
)

func run(ctx context.Context) error {
	path := flag.String("config", "", "observer JSON config (no credentials)")
	database := flag.String("db", "", "absolute private feishu-observer.sqlite path")
	profile := flag.String("live-profile", "", "explicit configured Lark CLI bot profile; without this flag read stdin only")
	flag.Parse()
	if *path == "" || *database == "" || flag.NArg() != 0 {
		return errors.New("provide --config and --db")
	}
	configFile, err := os.Open(*path)
	if err != nil {
		return err
	}
	defer configFile.Close()
	var c feishuobserve.Config
	decoder := json.NewDecoder(io.LimitReader(configFile, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return errors.New("cannot decode observer config")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("trailing data in observer config")
	}
	if err := c.Validate(); err != nil {
		return err
	}
	db, err := feishuobserve.Open(*database)
	if err != nil {
		return err
	}
	defer db.Close()

	var input io.Reader = os.Stdin
	var child *exec.Cmd
	var activated time.Time
	if *profile != "" {
		// Explicit opt-in. Do not print raw CLI stderr or event bodies.
		child = exec.CommandContext(ctx, "lark-cli", "event", "consume", "im.message.receive_v1", "--as", "bot", "--profile", *profile)
		input, err = child.StdoutPipe()
		if err != nil {
			return err
		}
		reader, writer, err := os.Pipe()
		if err != nil {
			return err
		}
		defer reader.Close()
		defer writer.Close()
		child.Stdin = reader // Avoid premature stdin EOF in a background service.
		activated = time.Now()
		if err := child.Start(); err != nil {
			return errors.New("cannot start configured lark-cli consumer")
		}
		defer func() { child.Process.Kill(); child.Wait() }()
	}

	scan := bufio.NewScanner(input)
	scan.Buffer(make([]byte, 64<<10), 1<<20)
	for scan.Scan() {
		var event feishuobserve.Event
		if json.Unmarshal(scan.Bytes(), &event) != nil {
			fmt.Fprintln(os.Stderr, "invalid-event")
			continue
		}
		var outcome string
		if c.PMReview {
			if child != nil && !feishuobserve.FreshPMEvent(event, activated, time.Now()) {
				continue
			}
			outcome, err = c.QueuePMReview(db, event)
		} else {
			var record feishuobserve.Staged
			record, outcome = c.Project(event)
			if outcome == "matched" {
				outcome, err = feishuobserve.Stage(db, record)
			}
		}
		if err != nil {
			return errors.New("cannot stage event in local database")
		}
		fmt.Fprintln(os.Stderr, outcome)
	}
	if ctx.Err() != nil {
		return nil
	}
	if scan.Err() != nil {
		return errors.New("event input exceeded size limit or could not be read")
	}
	if child != nil {
		return errors.New("live consumer stopped; inspect its setup separately")
	}
	return nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}
