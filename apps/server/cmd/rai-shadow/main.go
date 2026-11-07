// Command rai-shadow runs the synthetic-only aggregate shadow comparison.
// It refuses non-synthetic input and never changes an active model assignment.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"rai/server/internal/shadow"
)

func main() {
	input := flag.String("input", "", "path to synthetic shadow JSON")
	output := flag.String("out", "-", "output path or - for stdout")
	flag.Parse()
	if *input == "" {
		fmt.Fprintln(os.Stderr, "rai-shadow: -input is required")
		os.Exit(2)
	}
	b, err := os.ReadFile(*input)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rai-shadow:", err)
		os.Exit(1)
	}
	var batch shadow.Batch
	if err := json.Unmarshal(b, &batch); err != nil {
		fmt.Fprintln(os.Stderr, "rai-shadow:", err)
		os.Exit(1)
	}
	r, err := shadow.Evaluate(batch)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rai-shadow:", err)
		os.Exit(1)
	}
	out, _ := json.MarshalIndent(r, "", "  ")
	out = append(out, '\n')
	if *output == "-" {
		_, err = os.Stdout.Write(out)
	} else {
		err = os.WriteFile(*output, out, 0o600)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "rai-shadow:", err)
		os.Exit(1)
	}
}
