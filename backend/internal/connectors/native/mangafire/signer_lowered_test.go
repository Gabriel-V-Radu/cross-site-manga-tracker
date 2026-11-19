package mangafire

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	esbuild "github.com/evanw/esbuild/pkg/api"
)

// updateLoweredSigner rewrites signer_bundle.lowered.js instead of checking
// it:
//
//	go test ./internal/connectors/native/mangafire/ -run TestLoweredSignerBundleIsCurrent -update-signer
var updateLoweredSigner = flag.Bool("update-signer", false, "rewrite signer_bundle.lowered.js from signer_bundle.js")

const (
	signerBundleSourceFile  = "signer_bundle.js"
	signerBundleLoweredFile = "signer_bundle.lowered.js"
	loweredSignerHeader     = "// Code generated from signer_bundle.js by TestLoweredSignerBundleIsCurrent -update-signer; DO NOT EDIT.\n"
)

// lowerSignerBundle is the transform the server used to run at start-up, on
// its first request to MangaFire: esbuild lowers the upstream bundle's modern
// syntax (async generators, an ESM export) to what goja parses. It lives in
// the tests now. The server embeds the result, so esbuild — 4.7 MB of the
// server's 38 on the Pi's linux/arm64, measured when this moved — is neither
// linked into it nor compiled for the image.
func lowerSignerBundle(source string) (string, error) {
	result := esbuild.Transform(source, esbuild.TransformOptions{
		Loader: esbuild.LoaderJS,
		Format: esbuild.FormatIIFE,
		Target: esbuild.ES2017,
	})
	if len(result.Errors) > 0 {
		messages := make([]string, 0, len(result.Errors))
		for _, message := range result.Errors {
			messages = append(messages, message.Text)
		}
		return "", fmt.Errorf("transpile mangafire signer: %s", strings.Join(messages, "; "))
	}
	return loweredSignerHeader + string(result.Code), nil
}

// TestLoweredSignerBundleIsCurrent pins the embedded, lowered signer to the
// upstream bundle beside it: lowering signer_bundle.js again must give the
// bytes the server embeds. That is what keeps the two from drifting — a
// refreshed signer_bundle.js whose lowered copy was not regenerated would
// leave the server signing with the old one — and it is where an esbuild
// upgrade that lowers differently shows up, to be regenerated on purpose.
// TestSignerMatchesBrowserVectors then checks that what is embedded mints the
// tokens the browser does.
func TestLoweredSignerBundleIsCurrent(t *testing.T) {
	source, err := os.ReadFile(signerBundleSourceFile)
	if err != nil {
		t.Fatalf("read the upstream bundle: %v", err)
	}
	lowered, err := lowerSignerBundle(string(source))
	if err != nil {
		t.Fatal(err)
	}

	if *updateLoweredSigner {
		if err := os.WriteFile(signerBundleLoweredFile, []byte(lowered), 0o644); err != nil {
			t.Fatalf("write %s: %v", signerBundleLoweredFile, err)
		}
		t.Logf("wrote %s (%d bytes); run the tests again without -update-signer", signerBundleLoweredFile, len(lowered))
		return
	}

	if signerBundleLowered == lowered {
		return
	}
	hint := "regenerate it with: go test ./internal/connectors/native/mangafire/ -run TestLoweredSignerBundleIsCurrent -update-signer"
	if strings.ReplaceAll(signerBundleLowered, "\r\n", "\n") == lowered {
		hint = "it differs only in line endings: the file must be checked out as it is stored (see .gitattributes), not converted to CRLF"
	}
	t.Fatalf("%s (%d bytes) is not %s lowered (%d bytes); %s",
		signerBundleLoweredFile, len(signerBundleLowered), signerBundleSourceFile, len(lowered), hint)
}

// TestLoweredSignerNeedsNoTranspilerAtRuntime keeps esbuild out of the server:
// nothing but the tests may import it, or the binary carries it again.
func TestLoweredSignerNeedsNoTranspilerAtRuntime(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("list the package: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(source), `"github.com/evanw/esbuild`) {
			t.Errorf("%s imports esbuild: the signer is lowered ahead of time so the server does not link it", name)
		}
	}
}
