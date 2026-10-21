package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/url"
	"os"
	"reflect"
	"regexp"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

var allocationPolicyPin = regexp.MustCompile(`^[a-f0-9]{64}$`)

func runAllocationPolicy(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	receiver := fs.String("receiver-id", "", "explicit service receiver identity")
	var id, file, pin, account string
	if command == "allocation-policy-register" {
		fs.StringVar(&account, "account-id", "", "owner-selected paper account ID")
		fs.StringVar(&file, "file", "", "bounded allocation policy JSON")
	}
	if command == "allocation-policy" {
		fs.StringVar(&id, "id", "", "allocation policy ID")
		fs.StringVar(&pin, "record-sha256", "", "expected full policy record SHA-256")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 || !storage.ValidReceiverID(*receiver) {
		return allocation.ErrInvalid
	}
	if command == "allocation-policies" {
		var c allocation.PolicyCatalog
		if err = requestBounded(ctx, *base, "/v1/allocation-policies", nil, &c, nil, allocation.MaxPolicyCatalogBytes, true); err != nil {
			return err
		}
		if c.Validate() != nil || c.ReceiverID != *receiver {
			return allocation.ErrInvalid
		}
		return emit(out, c)
	}
	if command == "allocation-policy" {
		if !paperaccount.ID.MatchString(id) || !allocationPolicyPin.MatchString(pin) {
			return allocation.ErrInvalid
		}
		var r allocation.PolicyRecord
		if err = requestBounded(ctx, *base, "/v1/allocation-policies/"+id, nil, &r, nil, allocation.MaxPolicyRecordBytes, true); err != nil {
			return err
		}
		if r.Validate() != nil || r.Command.Policy.ID != id || r.Command.ReceiverID != *receiver || r.SHA256 != pin {
			return allocation.ErrInvalid
		}
		return emit(out, r)
	}
	if !paperaccount.ID.MatchString(account) || file == "" {
		return allocation.ErrInvalid
	}
	f, err := os.Open(file)
	if err != nil {
		return allocation.ErrInvalid
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, allocation.MaxPolicyBytes+1))
	err = errors.Join(readErr, f.Close())
	var policy allocation.Policy
	if err != nil || workerplan.ReadBoundedShape(raw, &policy, allocation.MaxPolicyBytes) != nil {
		return allocation.ErrInvalid
	}
	cmd := allocation.PolicyCommand{ReceiverID: *receiver, AccountID: account, Policy: policy}
	if cmd.Validate() != nil {
		return allocation.ErrInvalid
	}
	var receipt allocation.PolicyReceipt
	if err = requestBounded(ctx, *base, "/v1/allocation-policies", cmd, &receipt, nil, allocation.MaxPolicyRecordBytes, true); err != nil {
		return err
	}
	if receipt.Record.Validate() != nil || !reflect.DeepEqual(receipt.Record.Command, cmd) {
		return allocation.ErrInvalid
	}
	return emit(out, receipt)
}
