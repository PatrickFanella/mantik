package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

const defaultURL = "http://127.0.0.1:40880"

func Run(ctx context.Context, args []string, out, errOut io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		_, err := fmt.Fprintln(out, "Mantik — autonomous paper research bootstrap\n\nCommands:\n  serve    Persistent PostgreSQL-backed loopback service\n  demo     Run synthetic entry/stop lifecycle (memory, or --api-url URL)\n  status   Inspect service state (--format table|json)\n  observe  Submit fixture observation JSON from stdin\n  replay   Run a sealed synthetic dataset at explicit cutoffs\n\nUse COMMAND --help for flags. All execution is synthetic paper.")
		return err
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(errOut)
	var apiURL, format, listen, datasetPath, cutoffText, datasetDigest string
	var exportDataset bool
	switch args[0] {
	case "serve":
		fs.StringVar(&listen, "listen", "127.0.0.1:40880", "listen address; non-loopback requires MANTIK_API_TOKEN")
	case "demo":
		fs.StringVar(&apiURL, "api-url", "", "service URL; omit for in-memory demo")
	case "status", "observe":
		fs.StringVar(&apiURL, "api-url", defaultURL, "service URL")
	case "replay":
		fs.StringVar(&datasetPath, "dataset", "", "sealed dataset JSON file; omit for the synthetic fixture")
		fs.StringVar(&datasetDigest, "dataset-sha256", "", "expected content identity (required with dataset)")
		fs.StringVar(&cutoffText, "cutoffs", "", "comma-separated, strictly increasing RFC3339 cutoffs (required for replay)")
		fs.BoolVar(&exportDataset, "export-dataset", false, "print the built-in sealed synthetic dataset without replay")
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	if args[0] == "status" {
		fs.StringVar(&format, "format", "table", "table or json")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if args[0] == "serve" {
		return serve(ctx, listen)
	}
	if args[0] == "replay" {
		if exportDataset {
			if datasetPath != "" || cutoffText != "" || datasetDigest != "" {
				return errors.New("export-dataset cannot be combined with dataset or cutoffs")
			}
			d, err := replay.FixtureDataset()
			if err != nil {
				return err
			}
			return emit(out, d)
		}
		d, err := replay.FixtureDataset()
		if (datasetPath == "") != (datasetDigest == "") {
			return errors.New("dataset and dataset-sha256 must be provided together")
		}
		if datasetPath != "" {
			file, openErr := os.Open(datasetPath)
			if openErr != nil {
				return openErr
			}
			d, err = replay.Read(file)
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
		}
		if err != nil {
			return err
		}
		if datasetDigest != "" && d.SHA256 != datasetDigest {
			return errors.New("dataset does not match the expected content identity")
		}
		if cutoffText == "" {
			return errors.New("replay requires explicit cutoffs")
		}
		var cutoffs []time.Time
		for _, value := range strings.Split(cutoffText, ",") {
			at, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return fmt.Errorf("invalid replay cutoff %q", value)
			}
			cutoffs = append(cutoffs, at)
		}
		result, err := replay.Run(ctx, d, replay.FixturePins(), cutoffs)
		if err != nil {
			return err
		}
		return emit(out, result)
	}
	if apiURL != "" {
		u, err := url.Parse(apiURL)
		if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
			return errors.New("api-url must be an HTTP origin without credentials or a path")
		}
	}
	switch args[0] {
	case "demo":
		var snapshot domain.Snapshot
		if apiURL == "" {
			store := storage.NewMemory()
			for _, o := range workflow.Fixtures() {
				if _, err := store.Ingest(ctx, o); err != nil {
					return err
				}
			}
			var err error
			snapshot, err = store.Snapshot(ctx)
			if err != nil {
				return err
			}
		} else {
			for _, o := range workflow.Fixtures() {
				var receipt domain.Receipt
				if err := request(ctx, apiURL, "/v1/observations", o, &receipt); err != nil {
					return err
				}
			}
			if err := request(ctx, apiURL, "/v1/status", nil, &snapshot); err != nil {
				return err
			}
		}
		return emit(out, snapshot)
	case "status":
		if format != "json" && format != "table" {
			return errors.New("format must be table or json")
		}
		var snapshot domain.Snapshot
		if err := request(ctx, apiURL, "/v1/status", nil, &snapshot); err != nil {
			return err
		}
		if format == "json" {
			return emit(out, snapshot)
		}
		_, err := fmt.Fprintf(out, "MODE          %s\nCASE          %s\nSTATE         %s\nOBSERVATIONS  %d\nEVENTS        %d\n", snapshot.Mode, snapshot.State.Case.ID, snapshot.State.Case.Status, snapshot.ObservationCount, len(snapshot.Events))
		if err != nil {
			return err
		}
		if exit := snapshot.State.Case.Exit; exit != nil {
			_, err = fmt.Fprintf(out, "REALIZED PNL  %d cents (synthetic, zero costs)\n", exit.RealizedPnLCents)
		}
		return err
	case "observe":
		decoder := json.NewDecoder(io.LimitReader(os.Stdin, 16385))
		decoder.DisallowUnknownFields()
		var o domain.Observation
		if err := decoder.Decode(&o); err != nil {
			return errors.New("invalid observation JSON on stdin")
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return errors.New("expected one observation on stdin")
		}
		var receipt domain.Receipt
		if err := request(ctx, apiURL, "/v1/observations", o, &receipt); err != nil {
			return err
		}
		return emit(out, receipt)
	}
	return nil
}

func serve(ctx context.Context, listen string) error {
	host, _, err := net.SplitHostPort(listen)
	token := os.Getenv("MANTIK_API_TOKEN")
	if err != nil || net.ParseIP(host) == nil {
		return errors.New("serve requires a numeric IP listen address")
	}
	if !net.ParseIP(host).IsLoopback() && len(token) < 16 {
		return errors.New("non-loopback serve requires MANTIK_API_TOKEN of at least 16 characters")
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required for serve")
	}
	openCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	store, err := storage.OpenPostgres(openCtx, databaseURL)
	cancel()
	if err != nil {
		return err
	}
	defer store.Close()
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	server := &http.Server{Handler: service.Handler(store, token), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	select {
	case err := <-finished:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return err
		}
		return nil
	}
}

func request(ctx context.Context, base, path string, input, output any) error {
	method := http.MethodGet
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
		method = http.MethodPost
	}
	u, err := url.Parse(base)
	if err != nil {
		return err
	}
	u.Path = path
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token := os.Getenv("MANTIK_API_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return errors.New("cannot reach Mantik service")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 16384)).Decode(&failure)
		return fmt.Errorf("Mantik HTTP %d: %s", res.StatusCode, failure.Error)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(output)
}

func emit(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
