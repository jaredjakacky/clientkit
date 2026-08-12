package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestCompositionUsesWorkerkitChecksAndPassiveServekitReads(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var output bytes.Buffer
	if err := run(ctx, &output); err != nil {
		t.Fatal(err)
	}

	for _, expected := range []string{
		"before readyz_status=503 health_checks=0",
		"after readyz_status=200 health_checks=1",
		"inspection unauthorized_status=401 authorized_status=200 health_checks=1",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("output %q does not contain %q", output.String(), expected)
		}
	}
}
