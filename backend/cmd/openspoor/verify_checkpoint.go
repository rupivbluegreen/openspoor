package main

import (
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/openspoor/openspoor/internal/core/checkpoint"
)

const verifyCheckpointUsage = `openspoor verify-checkpoint [--pubkey KEY|@FILE] [--json] FILE...

Verifies exported tamper-evidence checkpoints offline (no database, no network): the Ed25519
signature, the link between consecutive checkpoints and, when the export carries evidence, the
audit hash chain slice and the Merkle root of the event batch.
Without --pubkey only integrity is checked, not who signed; pass the node's public key
(shown by the checkpoint export and by GET /api/v1/trace/integrity/node) to check origin.
Exit status: 0 all checks passed, 1 a check failed, 2 usage or unreadable input.
`

// runVerifyCheckpoint implements the offline `verify-checkpoint` subcommand.
func runVerifyCheckpoint(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify-checkpoint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pub := fs.String("pubkey", "", "trusted node public key (base64url), or @file containing it")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	fs.Usage = func() { fmt.Fprint(stderr, verifyCheckpointUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return 2
	}
	var trusted ed25519.PublicKey
	if *pub != "" {
		s := *pub
		if strings.HasPrefix(s, "@") {
			b, err := os.ReadFile(s[1:]) //nolint:gosec // the operator names the key file
			if err != nil {
				fmt.Fprintln(stderr, "verify-checkpoint:", err)
				return 2
			}
			s = string(b)
		}
		k, err := checkpoint.ParsePublicKey(s)
		if err != nil {
			fmt.Fprintln(stderr, "verify-checkpoint:", err)
			return 2
		}
		trusted = k
	}
	var bundles []checkpoint.Bundle
	for _, name := range fs.Args() {
		b, err := os.ReadFile(name) //nolint:gosec // the operator names the checkpoint files to verify
		if err != nil {
			fmt.Fprintln(stderr, "verify-checkpoint:", err)
			return 2
		}
		var bundle checkpoint.Bundle
		if err := json.Unmarshal(b, &bundle); err != nil || bundle.Checkpoint.Type == "" {
			fmt.Fprintf(stderr, "verify-checkpoint: %s is not a checkpoint export\n", name)
			return 2
		}
		bundles = append(bundles, bundle)
	}
	results := checkpoint.VerifyChain(bundles, trusted)
	ok := true
	for _, r := range results {
		ok = ok && r.OK
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"ok": ok, "results": results})
	} else {
		for _, r := range results {
			fmt.Fprintf(stdout, "checkpoint %d\n", r.Seq)
			for _, c := range r.Checks {
				mark := "ok  "
				if !c.OK {
					mark = "FAIL"
				}
				fmt.Fprintf(stdout, "  %s %s: %s\n", mark, c.Name, c.Detail)
			}
		}
		if ok {
			fmt.Fprintln(stdout, "all checks passed")
		} else {
			fmt.Fprintln(stdout, "VERIFICATION FAILED")
		}
	}
	if !ok {
		return 1
	}
	return 0
}
