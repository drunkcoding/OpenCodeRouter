package main

import (
	"log/slog"
	"strings"
	"testing"
)

func TestParseLikelyOrphansFromLsofOutputFiltersToOpencodeAndRange(t *testing.T) {
	raw := strings.Join([]string{
		"p101",
		"copencode",
		"n127.0.0.1:30010",
		"n127.0.0.1:30010",
		"p202",
		"cnode",
		"n127.0.0.1:30011",
		"p303",
		"copencode",
		"n127.0.0.1:29999",
		"",
	}, "\n")

	orphans := parseLikelyOrphansFromLsofOutput(raw, 30000, 30020)
	if len(orphans) != 1 {
		t.Fatalf("orphans len=%d want=1 (%#v)", len(orphans), orphans)
	}
	if orphans[0].PID != 101 || orphans[0].Port != 30010 {
		t.Fatalf("unexpected orphan %#v", orphans[0])
	}
}

func TestExtractListenPort(t *testing.T) {
	port, ok := extractListenPort("127.0.0.1:31000")
	if !ok || port != 31000 {
		t.Fatalf("extract simple addr got (%d,%v) want (31000,true)", port, ok)
	}

	port, ok = extractListenPort("127.0.0.1:31001->127.0.0.1:51514")
	if !ok || port != 31001 {
		t.Fatalf("extract connected addr got (%d,%v) want (31001,true)", port, ok)
	}

	if _, ok := extractListenPort("nonsense"); ok {
		t.Fatal("expected invalid addr to return ok=false")
	}
}

func TestHandleStartupOrphanOfferNoCleanupByDefault(t *testing.T) {
	logger := slog.Default()
	handleStartupOrphanOffer(31010, 31000, false, logger)
}
