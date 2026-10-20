package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
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
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

const defaultURL = "http://127.0.0.1:40880"

func Run(ctx context.Context, args []string, out, errOut io.Writer) error {
	if len(args) > 0 && (args[0] == "candidate-register" || args[0] == "candidate-registration" || args[0] == "candidate-registrations" || args[0] == "candidate-dataset") {
		return runCandidateRegistry(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "candidate-replay-plan" || args[0] == "candidate-replay-input") {
		return runCandidateReplay(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "candidate-property-plan" || args[0] == "candidate-property-evaluate") {
		return runCandidateProperty(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "candidate-attempt-run" || args[0] == "candidate-attempt-reconcile" || args[0] == "candidate-attempt-history") {
		return runCandidateJournal(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "candidate-test-plan" || args[0] == "candidate-test-evaluate") {
		return runCandidateTest(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "candidate-behavior-job" || args[0] == "candidate-behavior-launch" || args[0] == "candidate-behavior-reconcile" || args[0] == "candidate-behavior-result" || args[0] == "candidate-behavior-execute") {
		return runCandidateBehavior(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "candidate-build-job" || args[0] == "candidate-build-launch" || args[0] == "candidate-build-reconcile" || args[0] == "candidate-build-result" || args[0] == "candidate-build-execute") {
		return runCandidateBuild(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "candidate-seal" || args[0] == "candidate-inspect") {
		return runCandidate(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "fixture-capital" || args[0] == "fixture-capital-bind") {
		return runFixtureCapital(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "paper-ledger-record" || args[0] == "paper-ledger-rehearsal") {
		return runPaperLedgerRehearsal(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "paper-ledger-audit" {
		return runPaperLedger(ctx, args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "paper-account-create" || args[0] == "paper-accounts" || args[0] == "paper-account") {
		return runPaperAccount(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "news-reviews" {
		return runReviewHistory(ctx, args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "trial-research-history" || args[0] == "hypothesis-research-history") {
		return runResearchHistory(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "trial-worker-attempt-start" || args[0] == "trial-worker-attempt-report" || args[0] == "trial-worker-attempt" || args[0] == "trial-worker-attempts" || args[0] == "trial-worker-attempt-output") {
		return runExternalAttempt(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "trial-worker-declare" || args[0] == "trial-worker" || args[0] == "trial-worker-input") {
		return runTrialWorker(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "external-worker-job" || args[0] == "external-worker-launch" || args[0] == "external-worker-reconcile" || args[0] == "external-worker-result") {
		return runExternalWorker(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "worker-launch" || args[0] == "worker-reconcile") {
		return runWorkerLaunch(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "worker-job" || args[0] == "worker-execute" || args[0] == "worker-result") {
		return runResearchWorker(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "trial-run-execute" || args[0] == "trial-run-resolve" || args[0] == "trial-run" || args[0] == "trial-assessment" || args[0] == "trial-assessments") {
		return runTrialExecution(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "trial-attempt-start" || args[0] == "trial-attempt-cancel" || args[0] == "trial-attempt" || args[0] == "trial-attempts") {
		return runTrialAttempt(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "hypothesis-trial-register" || args[0] == "hypothesis-trial" || args[0] == "hypothesis-trials" || args[0] == "hypothesis-assessments") {
		return runTrial(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "hypothesis-publish" || args[0] == "hypothesis" || args[0] == "hypotheses") {
		return runHypothesisPublication(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "hypothesis-declare" {
		return runHypothesis(ctx, args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "news-review" || args[0] == "news-review-start" || args[0] == "news-review-complete") {
		return runNewsReview(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "research-review" {
		return runResearchReview(ctx, args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "news-submit" || args[0] == "news-cases" || args[0] == "news-record" || args[0] == "news-close") {
		return runNewsService(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "news-retain" || args[0] == "news-journal" || args[0] == "news-inspect" || args[0] == "news-events") {
		return runNewsJournal(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "news-normalize" {
		return runNews(ctx, args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "news-switchyard-admit" || args[0] == "news-switchyard-admission") {
		return runSwitchyardCaptureAdmission(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "news-local-admit" || args[0] == "news-local-admission") {
		return runLocalCaptureAdmission(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "news-switchyard-import" || args[0] == "news-switchyard-capture" || args[0] == "news-switchyard-captures") {
		return runSwitchyardCaptures(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "news-local-import" || args[0] == "news-local-capture" || args[0] == "news-local-captures") {
		return runLocalCaptures(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "news-extraction-start" || args[0] == "news-extraction-complete" || args[0] == "news-extraction" || args[0] == "news-extractions") {
		return runNewsExtractions(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "news-extraction-admit" || args[0] == "news-extraction-admission") {
		return runExtractionAdmission(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "news-extraction-evaluate" {
		return runExtractionEvaluation(ctx, args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "news-switchyard-evaluate" {
		return runSwitchyardEvaluation(ctx, args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "news-local-evaluate" {
		return runLocalEvaluation(ctx, args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "news-switchyard-inspect" {
		return runCapturedInspection(ctx, args[1:], out, errOut, true)
	}
	if len(args) > 0 && args[0] == "news-extract-switchyard" {
		return runNewsExtractCaptured(ctx, args[1:], out, errOut, true)
	}
	if len(args) > 0 && args[0] == "news-local-inspect" {
		return runLocalInspection(ctx, args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "news-extract-local" {
		return runNewsExtractLocal(ctx, args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "news-extract" {
		return runNewsExtract(ctx, args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "library" {
		return runLibrary(ctx, args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "exceptions" {
		return runExceptions(ctx, args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "cases" {
		return runCases(ctx, args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "supervise" {
		return runSupervise(ctx, args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "activity" {
		return runActivity(ctx, args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "feed-coverage" {
		return runFeedCoverage(ctx, args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "alpaca-publish" {
		return runAlpacaPublish(ctx, args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "alpaca-run-collector" || args[0] == "alpaca-collector-status") {
		return runAlpacaCollector(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "alpaca-coverage" {
		return runAlpacaCoverage(ctx, args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "alpaca-archive" || args[0] == "alpaca-archive-verify" || args[0] == "alpaca-restore") {
		return runAlpacaArchive(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "alpaca-collect" || args[0] == "alpaca-collection" || args[0] == "alpaca-session" || args[0] == "alpaca-session-status") {
		return runAlpacaCollect(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "operations" || args[0] == "readiness" || args[0] == "recover") {
		return runOperational(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "trigger-register" || args[0] == "route-input" || args[0] == "close-routing-case") {
		return runRouting(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && (args[0] == "alpaca-retain" || args[0] == "alpaca-submit" || args[0] == "alpaca-journal") {
		return runAlpacaJournal(ctx, args[0], args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "alpaca-normalize" {
		return runAlpaca(ctx, args[1:], out, errOut)
	}
	if len(args) > 0 && args[0] == "experiment" {
		return runExperiment(ctx, args[1:], out, errOut)
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		_, err := fmt.Fprintln(out, "Mantik — autonomous paper research bootstrap\n\nCommands:\n  serve    Persistent PostgreSQL-backed loopback service\n  demo     Run synthetic entry/stop lifecycle (memory, or --api-url URL)\n  status   Inspect service state (--format table|json)\n  activity Inspect a bounded page of retained audit events\n  supervise Interactive read-only activity, cases, news, portfolio, exceptions and library\n  cases    Inspect current fixture and routing cases with retained references\n  observe  Submit fixture observation JSON from stdin\n  replay   Run a sealed synthetic dataset at explicit cutoffs\n  alpaca-run-collector  Acquire, renew and release a lease for finite quote collection/delivery/publication\n  alpaca-collector-status  Inspect retained finite collector history\n  alpaca-publish  Publish one validated retained capture under a collector fence\n  feed-coverage  Inspect server-validated evidence and collector state\n  alpaca-coverage  Inspect retained regular-session calendar/quote coverage\n  alpaca-archive  Copy all journal evidence to a new private archive\n  alpaca-archive-verify  Verify an archive against its expected SHA-256\n  alpaca-restore  Restore and audit an isolated journal copy\n  alpaca-session  Run a finite collection session within journal capacity\n  alpaca-session-status  Inspect retained session history\n  alpaca-collect  Acquire one bounded Alpaca read into a private journal\n  alpaca-collection  Inspect retained acquisition status\n  alpaca-retain  Durably retain an attributed recording in a private journal\n  alpaca-submit  Submit bounded pending research inputs to a pinned receiver\n  alpaca-journal  Inspect local retained and pending capture counts\n  alpaca-normalize  Normalize retained Alpaca price/calendar recordings\n  experiment  Compare synthetic baselines and retain attempts\n  trial-research-history Inspect native assessments and every retained external attempt\n  hypothesis-research-history Inspect combined research history across all plans\n  trial-worker-attempt-start Retain an external execution intent\n  trial-worker-attempt-report Retain a caller-reported external outcome\n  trial-worker-attempt Inspect an external intent and reported outcome\n  trial-worker-attempts Inspect every retained attempt for one external job\n  trial-worker-attempt-output Export exact reported stdout or stderr\n  trial-worker-declare Retain an external job bound to a native trial\n  trial-worker Inspect a retained external job declaration\n  trial-worker-input Inspect or export one pinned external job input\n  external-worker-job Declare a pinned Python fixture comparison\n  external-worker-launch Run one owned external comparison attempt\n  external-worker-reconcile Inspect or cancel an owned external attempt\n  external-worker-result Validate exact external comparison bytes\n  worker-job Declare a pinned offline native-fixture worker job\n  worker-execute Execute a validated native-fixture job without service access\n  worker-result Validate a worker result against its job identity\n  worker-launch Launch one explicit private worker attempt in local Docker\n  worker-reconcile Inspect or explicitly cancel one retained owned attempt\n  library  Inspect the service's compiled synthetic methods and contracts\n  news-extraction-admit Approve exact retained candidates for provenance-bound research admission\n  news-extraction-admission Inspect reviewed admission and verify its retained links\n  news-extraction-start Retain an article, event key and extraction deadline\n  news-extraction-complete Complete a retained extraction with exact recorded bytes\n  news-extraction Inspect a retained extraction attempt and result\n  news-extractions Inspect every retained extraction attempt\n  news-extraction-evaluate Compare all recorded outputs with supplied expected claims\n  news-local-admit Approve exact retained local candidates for research\n  news-local-admission Verify local capture approval and news provenance\n  news-local-import Retain caller-reported local captures without model calls\n  news-local-capture Inspect one retained local capture with full evidence\n  news-local-captures Inspect compact local capture history\n  news-local-evaluate Compare every retained local capture with supplied annotations\n  news-local-inspect Verify retained local extraction receipts without network calls\n  news-extract-switchyard Retain one selected OpenAI route request through Switchyard\n  news-switchyard-evaluate Compare all retained Switchyard captures with supplied annotations\n  news-switchyard-admit Approve exact retained Switchyard candidates for research\n  news-switchyard-admission Verify Switchyard approval and news provenance\n  news-switchyard-import Retain caller-reported Switchyard captures without model calls\n  news-switchyard-capture Inspect a retained Switchyard capture\n  news-switchyard-captures Inspect compact Switchyard capture history\n  news-switchyard-inspect Verify retained Switchyard receipts offline\n  news-extract-local Retain one bounded local-model extraction request and result\n  news-extract Check recorded extractor output against a source-only article\n  news-normalize Validate recorded news claims against exact source excerpts\n  news-retain Retain a source/claim revision in a private append-only journal\n  news-journal Audit retained news records and revision chains\n  news-events Inspect declared event groups at an explicit receipt cutoff\n  trial-run-execute Execute one retained intent with the native synthetic fixture\n  trial-run Inspect retained run metadata or a targeted artifact\n  trial-run-resolve Record a missing durable result after its deadline\n  trial-assessment Compare retained native metrics with declared conditions\n  trial-assessments Inspect all attempt assessments for one registered trial\n  trial-attempt-start Retain a pre-execution attempt intent\n  trial-attempt-cancel Cancel an intent before execution\n  trial-attempt Inspect one retained attempt intent\n  trial-attempts Inspect all intents for one registered trial\n  hypothesis-trial-register Retain a publication-linked experiment plan\n  hypothesis-trial Inspect one registered trial plan\n  hypothesis-trials Inspect planned trial lineage for a hypothesis\n  hypothesis-assessments Inspect all plans and attempt assessments for a hypothesis\n  hypothesis-publish Retain a declaration bound to a current service review\n  hypotheses Discover every retained question and publication\n  hypothesis Inspect all retained publications for one exact question\n  hypothesis-declare Validate and hash a pre-test synthetic hypothesis declaration\n  candidate-register Retain a publication-bound candidate and exact dataset\n  candidate-registration Inspect one receiver and registration pin\n  candidate-registrations Inspect the complete receiver-owned candidate registry\n  candidate-dataset Export a pinned retained dataset with exact bytes\n  candidate-seal Pin caller-supplied research source without compiling or executing it\n  candidate-inspect Validate a sealed source declaration and optional exact dataset\n  candidate-build-job Declare a fixed compile-only candidate check\n  candidate-build-launch Compile one candidate in a credential-free owned container\n  candidate-build-reconcile Inspect/recover one retained candidate compile attempt\n  candidate-build-result Validate a retained compile-only worker result\n  candidate-replay-plan Derive a cutoff-visible synthetic candidate job\n  candidate-replay-input Export verified replay inputs with exact dataset bytes\n  candidate-property-plan Seal finite sample property requirements\n  candidate-property-evaluate Check every eligible sample pair in a retained worker result\n  candidate-attempt-run Retain intent and execute or reconcile a candidate request\n  candidate-attempt-reconcile Collect the original request handle without relaunching\n  candidate-attempt-history Inspect every local candidate attempt and pending intent\n  candidate-behavior-job Declare a pinned synthetic behavior worker\n  candidate-behavior-launch Execute a candidate in a bounded owned container\n  candidate-behavior-reconcile Inspect/recover a retained behavior attempt\n  candidate-behavior-result Validate a retained synthetic behavior result\n  candidate-test-plan Seal explicit synthetic signal expectations for every dataset record\n  candidate-test-evaluate Compare caller-reported outputs without executing source\n  research-review Run a bounded recorded-model review over targeted news excerpts\n  news-review-start Pin and retain a service case review attempt\n  news-review-complete Complete a retained attempt using recorded model artifacts\n  paper-ledger-rehearsal Inspect one durable synthetic account ledger rehearsal\n  paper-ledger-record Record one explicit pinned synthetic ledger entry\n  paper-ledger-audit Replay a recorded synthetic reservation ledger offline\n  fixture-capital Inspect validated synthetic fixture cash and position\n  fixture-capital-bind Bind a pristine fixture to explicit paper funding\n  paper-account-create Retain finite operator-declared paper capital\n  paper-accounts List bounded paper account configurations\n  paper-account Inspect one paper funding receipt\n  news-reviews Inspect every retained review for one news case\n  news-review Inspect a retained attempt and current applicability\n  news-submit Admit one retained news revision to a pinned research receiver\n  news-cases Inspect server news research cases and evidence versions\n  news-record Inspect one server-retained news record\n  news-close Close a news research case at its current evidence version\n  news-inspect Inspect retained claims, with original text only when requested\n  trigger-register  Register a fixture trigger from --file JSON\n  route-input  Submit attributed routing input from --file JSON\n  close-routing-case  Close an operator-owned case explicitly\n  operations  Inspect persisted exceptions and recovery attempts\n  exceptions  Page retained exceptions without loading audit history\n  readiness  Inspect a specific capability scope\n  recover  Attempt bounded exception recovery\n\nUse COMMAND --help for flags. All execution is synthetic paper.")
		return err
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(errOut)
	var apiURL, format, listen, datasetPath, cutoffText, datasetDigest, feedPolicyPath string
	var exportDataset bool
	switch args[0] {
	case "serve":
		fs.StringVar(&feedPolicyPath, "feed-readiness-policy", "", "optional receiver-bound feed readiness policy JSON file")
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
		return serve(ctx, listen, feedPolicyPath)
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

func serve(ctx context.Context, listen, policyPath string) error {
	var policy *service.FeedReadinessPolicy
	if policyPath != "" {
		file, err := os.Open(policyPath)
		if err != nil {
			return err
		}
		parsed, readErr := service.ReadFeedReadinessPolicy(file)
		closeErr := file.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return err
		}
		policy = parsed
	}

	host, _, err := net.SplitHostPort(listen)
	token := os.Getenv("MANTIK_API_TOKEN")
	if err != nil || net.ParseIP(host) == nil {
		return errors.New("serve requires a numeric IP listen address")
	}
	if !net.ParseIP(host).IsLoopback() && len(token) < 16 {
		return errors.New("non-loopback serve requires MANTIK_API_TOKEN of at least 16 characters")
	}
	interval := time.Second
	if value := os.Getenv("MANTIK_TIMER_INTERVAL"); value != "" {
		interval, err = time.ParseDuration(value)
		if err != nil || interval < 100*time.Millisecond || interval > time.Minute {
			return errors.New("MANTIK_TIMER_INTERVAL must be between 100ms and 1m")
		}
	}
	gate, err := service.NewFixtureActionGate(os.Getenv("MANTIK_FIXTURE_GATE_DIR"), os.Getenv("MANTIK_FIXTURE_ACTION_GATE"))
	if err != nil {
		return err
	}
	if gate != nil {
		slog.Warn("fixture action gate enabled; delivery may deliberately stall", "stage", os.Getenv("MANTIK_FIXTURE_ACTION_GATE"))
	}
	workerID := "worker-" + rand.Text()
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
	handler, err := service.HandlerWithFeedReadiness(store, token, service.WallClock{}, policy)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		service.RunTimerWorker(workerCtx, store, workerID, service.WallClock{}, interval, 30*time.Second)
	}()
	actionDone := make(chan struct{})
	go func() {
		defer close(actionDone)
		service.RunActionWorkerWithFixtureGate(workerCtx, store, workerID+"-actions", service.WallClock{}, interval, 30*time.Second, gate)
	}()
	defer func() { stopWorker(); <-workerDone; <-actionDone }()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
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
	return requestWithQuery(ctx, base, path, input, output, nil, 8<<20)
}

type httpFailure struct {
	Status  int
	Code    string
	Message string
}

func (e *httpFailure) Error() string { return fmt.Sprintf("Mantik HTTP %d: %s", e.Status, e.Message) }

func requestWithQuery(ctx context.Context, base, path string, input, output any, query url.Values, limit int64) error {
	return requestBounded(ctx, base, path, input, output, query, limit, false)
}

func requestBounded(ctx context.Context, base, path string, input, output any, query url.Values, limit int64, strict bool) error {
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
	if query != nil {
		u.RawQuery = query.Encode()
	}
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
			Code  string `json:"code"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 16384)).Decode(&failure)
		return &httpFailure{Status: res.StatusCode, Code: failure.Code, Message: failure.Error}
	}
	if strict {
		raw, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
		if err != nil {
			return err
		}
		return workerplan.ReadBoundedShape(raw, output, int(limit))
	}
	return json.NewDecoder(io.LimitReader(res.Body, limit)).Decode(output)
}

func emit(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
