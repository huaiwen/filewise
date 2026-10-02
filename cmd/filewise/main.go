//go:build darwin || linux

package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/huaiwen/filewise/internal/changes"
	"github.com/huaiwen/filewise/internal/documents"
	"github.com/huaiwen/filewise/internal/journal"
	"github.com/huaiwen/filewise/internal/workspace"
)

const version = "0.3.0-dev"

//go:embed THIRD_PARTY_NOTICES.txt
var notices string

const help = `Filewise Go development runtime

  filewise --version
  filewise --licenses
  filewise document inspect FILE
  filewise document compare --before BEFORE --after AFTER
  filewise document capture --state PRIVATE_DIR --base none|RECORD_ID FILE
  filewise document record --state PRIVATE_DIR --file FILE [--version latest|RECORD_ID]

capture stores immutable originals and meta.semantic_change; originals are never
modified. Use a separate private state directory for each document. Flags precede
FILE. These are local operator commands, not restricted Agent commands.

This document journal is separate from the project workspace and its HTTP
credentials. It is an operator tool, not an Agent authorization boundary.
`

func main() {
	if len(os.Args) == 3 && os.Args[1] == "__document_worker" {
		if documents.WorkerMain(os.Args[2]) != nil {
			os.Exit(1)
		}
		return
	}
	executable, err := os.Executable()
	if err == nil {
		err = documents.ConfigureWorker(executable)
	}
	if err == nil {
		err = run(os.Args[1:], os.Stdout, os.Stderr)
	}
	if err != nil {
		if workspace.Status(err) == 2 {
			os.Exit(2)
		}
		_ = json.NewEncoder(os.Stderr).Encode(map[string]string{"error": err.Error()})
		os.Exit(1)
	}
}
func run(args []string, out, diagnostic io.Writer) error {
	encode := func(v any) error {
		e := json.NewEncoder(out)
		e.SetIndent("", "  ")
		e.SetEscapeHTML(false)
		return e.Encode(v)
	}
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "help" || args[0] == "-h")) {
		_, err := io.WriteString(out, workspace.Help+"\n"+help)
		return err
	}
	if len(args) == 1 && args[0] == "--licenses" {
		_, err := io.WriteString(out, notices)
		return err
	}
	if len(args) == 1 && (args[0] == "--version" || args[0] == "version") {
		_, err := fmt.Fprintf(out, "filewise %s (Go)\n", version)
		return err
	}
	if args[0] != "document" {
		return workspace.CLI(context.Background(), args, os.Stdin, out, diagnostic)
	}
	if len(args) < 2 {
		return errors.New("Document command required; use --help")
	}
	f := flag.NewFlagSet(args[1], flag.ContinueOnError)
	f.SetOutput(diagnostic)
	switch args[1] {
	case "inspect":
		if err := f.Parse(args[2:]); err != nil {
			return err
		}
		if f.NArg() != 1 {
			return errors.New("inspect requires one FILE")
		}
		body, err := journal.ReadFile(f.Arg(0))
		if err != nil {
			return err
		}
		extraction := documents.Extract(f.Arg(0), body)
		if err = encode(struct {
			SHA256     string               `json:"sha256"`
			Extraction documents.Extraction `json:"extraction"`
		}{documents.Hash(body), extraction}); err != nil {
			return err
		}
		if extraction.Info.Status == "failed" || extraction.Info.Status == "unsupported" {
			return errors.New("Document text extraction unavailable; inspect the structured extraction status")
		}
		return nil
	case "compare":
		before := f.String("before", "", "previous file")
		after := f.String("after", "", "current file")
		if err := f.Parse(args[2:]); err != nil {
			return err
		}
		if *before == "" || *after == "" || f.NArg() != 0 {
			return errors.New("compare requires --before and --after, without positional arguments")
		}
		if !strings.EqualFold(filepath.Ext(*before), filepath.Ext(*after)) {
			return errors.New("Compare versions of the same file format")
		}
		a, err := journal.ReadFile(*before)
		if err != nil {
			return err
		}
		b, err := journal.ReadFile(*after)
		if err != nil {
			return err
		}
		return encode(journal.Meta{SemanticChange: changes.Derive(*after, a, b, true)})
	case "capture", "record":
		state := f.String("state", "", "private journal directory")
		var base, file, selector *string
		if args[1] == "capture" {
			base = f.String("base", "", "previous record ID or none")
		} else {
			file = f.String("file", "", "original document path")
			selector = f.String("version", "latest", "record ID or latest")
		}
		if err := f.Parse(args[2:]); err != nil {
			return err
		}
		if *state == "" {
			return errors.New("An explicit --state directory is required")
		}
		name := ""
		if args[1] == "capture" {
			if *base == "" || f.NArg() != 1 {
				return errors.New("capture requires --base and one FILE")
			}
			name = f.Arg(0)
		} else {
			if *file == "" || f.NArg() != 0 {
				return errors.New("record requires --file and no positional arguments")
			}
			name = *file
		}
		// Reading a record must not enroll a new document or initialize state.
		if args[1] == "record" {
			if _, err := os.Lstat(filepath.Join(*state, "format.json")); err != nil {
				return err
			}
		}
		j, err := journal.Open(*state, name)
		if err != nil {
			return err
		}
		defer j.Close()
		var receipt journal.Receipt
		if args[1] == "capture" {
			receipt, err = j.Capture(*base)
		} else {
			receipt, err = j.Read(*selector)
		}
		if err != nil {
			return err
		}
		return encode(receipt)
	default:
		return errors.New("Unknown document command; use --help")
	}
}
