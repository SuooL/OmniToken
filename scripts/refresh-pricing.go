//go:build ignore

// refresh-pricing regenerates internal/pricing/litellm_prices.json from
// LiteLLM's model_prices_and_context_window.json (ADR-0005).
//
// The embedded copy is a trim, not a mirror: LiteLLM's file is ~2.3MB of
// context windows, modalities and capability flags, of which this program keeps
// the five cost fields pricing.Price reads and nothing else. Upstream key order
// is preserved so a refresh produces a reviewable diff instead of a reshuffle.
//
//	go run scripts/refresh-pricing.go          # fetch upstream and rewrite
//	go run scripts/refresh-pricing.go -in f.json  # trim a local copy instead
//
// The result is a plain mirror of upstream: models upstream has dropped are
// dropped here too. Pricing an id upstream no longer carries is what
// `pricing_overrides` in the config file is for — inventing a local fork of the
// table would silently diverge from ccusage, which is the whole point of
// sourcing it from LiteLLM.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

const upstreamURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// costFields are exactly the fields pricing.Price unmarshals. Adding one here
// without adding it there grows the embedded file for no effect.
var costFields = []string{
	"input_cost_per_token",
	"output_cost_per_token",
	"cache_read_input_token_cost",
	"cache_creation_input_token_cost",
	"cache_creation_input_token_cost_above_1hr",
}

func main() {
	in := flag.String("in", "", "read upstream JSON from this file instead of fetching")
	out := flag.String("out", "internal/pricing/litellm_prices.json", "output path")
	flag.Parse()

	raw, err := load(*in)
	if err != nil {
		log.Fatalf("refresh-pricing: %v", err)
	}

	// json.RawMessage over an ordered decode: the upstream order is the diff
	// order, and a map would lose it.
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		log.Fatalf("refresh-pricing: parse upstream: %v", err)
	}
	keys, err := objectKeys(raw)
	if err != nil {
		log.Fatalf("refresh-pricing: read key order: %v", err)
	}

	var buf []byte
	buf = append(buf, '{')
	kept := 0
	for _, k := range keys {
		if k == "sample_spec" { // upstream's documentation stub, not a model
			continue
		}
		var entry map[string]json.RawMessage
		if json.Unmarshal(doc[k], &entry) != nil {
			continue // non-object values are not model entries
		}
		trimmed := trim(entry)
		if len(trimmed) == 0 {
			continue // no per-token price: image sizes, embeddings-only rows
		}
		if kept > 0 {
			buf = append(buf, ',')
		}
		kept++
		key, _ := json.Marshal(k)
		buf = append(buf, key...)
		buf = append(buf, ':', '{')
		for i, f := range trimmed {
			if i > 0 {
				buf = append(buf, ',')
			}
			name, _ := json.Marshal(f.name)
			buf = append(buf, name...)
			buf = append(buf, ':')
			buf = append(buf, f.value...)
		}
		buf = append(buf, '}')
	}
	buf = append(buf, '}')

	if !json.Valid(buf) {
		log.Fatal("refresh-pricing: generated file is not valid JSON")
	}
	if err := os.WriteFile(*out, buf, 0o644); err != nil {
		log.Fatalf("refresh-pricing: write %s: %v", *out, err)
	}
	fmt.Printf("refresh-pricing: %d models, %d bytes -> %s\n", kept, len(buf), *out)
}

type field struct {
	name  string
	value json.RawMessage
}

// trim keeps the cost fields in costFields order, dropping JSON nulls — an
// explicit null upstream means "not priced", which is the same as absent and
// would otherwise unmarshal to a zero rate that silently prices as free.
func trim(entry map[string]json.RawMessage) []field {
	out := make([]field, 0, len(costFields))
	for _, f := range costFields {
		v, ok := entry[f]
		if !ok || string(v) == "null" {
			continue
		}
		out = append(out, field{f, v})
	}
	return out
}

func load(path string) ([]byte, error) {
	if path != "" {
		return os.ReadFile(path)
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(upstreamURL)
	if err != nil {
		return nil, fmt.Errorf("fetch upstream: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch upstream: %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// objectKeys returns a JSON object's keys in document order, which
// encoding/json's map decoding discards.
func objectKeys(raw []byte) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("upstream is not a JSON object")
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("unexpected object key %v", tok)
		}
		keys = append(keys, key)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}
