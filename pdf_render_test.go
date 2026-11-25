package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDarwinPDFSlotDaemonReuseCrashRecoveryAndClose(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell mock renderer not supported on Windows")
	}
	dir := t.TempDir()
	pidLog := filepath.Join(dir, "pids.log")
	countFile := filepath.Join(dir, "count.txt")
	script := filepath.Join(dir, "mock-pdf")
	src := `#!/bin/sh
if [ "$1" = "--daemon" ]; then
  while IFS= read -r line; do
    n=0
    if [ -f "` + countFile + `" ]; then
      n=$(cat "` + countFile + `")
    fi
    n=$((n + 1))
    echo "$n" > "` + countFile + `"
    echo "$$:$n" >> "` + pidLog + `"
    if [ "$n" -eq 3 ]; then
      exit 1
    fi
    printf '{"count":12,"width":595,"height":842}\n'
  done
  exit 0
fi
echo "oneshot:$$" >> "` + pidLog + `"
printf '{"count":12,"width":595,"height":842}\n'
`
	if err := os.WriteFile(script, []byte(src), 0700); err != nil {
		t.Fatal(err)
	}

	slot := &darwinPDFSlot{idleTTL: time.Minute}
	defer slot.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Request 1 & 2 reuse the same daemon PID.
	for i := 0; i < 2; i++ {
		res, err := slot.render(ctx, script, "/tmp/doc.pdf", i, 1200, "/tmp/out.png")
		if err != nil || res.Count != 12 {
			t.Fatalf("request %d failed: res=%+v err=%v", i+1, res, err)
		}
	}
	// Request 3 triggers simulated daemon crash on request #3; slot transparently restarts & retries (#4).
	res, err := slot.render(ctx, script, "/tmp/doc.pdf", 2, 1200, "/tmp/out.png")
	if err != nil || res.Count != 12 {
		t.Fatalf("crash recovery failed: res=%+v err=%v", res, err)
	}

	slot.close()

	data, err := os.ReadFile(pidLog)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 daemon invocations (including 1 crash+retry), got %v", lines)
	}
	pid1 := strings.Split(lines[0], ":")[0]
	pid2 := strings.Split(lines[1], ":")[0]
	pid3 := strings.Split(lines[2], ":")[0]
	pid4 := strings.Split(lines[3], ":")[0]
	if pid1 != pid2 || pid1 != pid3 {
		t.Fatalf("expected requests 1..3 to share daemon PID, got %v", lines)
	}
	if pid4 == pid1 || strings.HasPrefix(lines[3], "oneshot:") {
		t.Fatalf("expected request 4 to restart daemon with new PID, got %v", lines)
	}
}

func TestPDFDaemonSlotSearchAndError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell mock daemon not supported on Windows")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "mock-search-pdf")
	src := `#!/bin/sh
if [ "$1" = "--daemon" ]; then
  while IFS= read -r line; do
    case "$line" in
      *bad-query*)
        printf '{"error":"索引损坏"}\n'
        ;;
      *)
        printf '{"count":8,"hits":[{"page":2,"snippet":"Euler identity"}]}\n'
        ;;
    esac
  done
fi
`
	if err := os.WriteFile(script, []byte(src), 0700); err != nil {
		t.Fatal(err)
	}

	slot := &pdfDaemonSlot{idleTTL: time.Minute}
	defer slot.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hits, err := slot.search(ctx, script, "/tmp/doc.pdf", "euler", 50)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(hits) != 1 || hits[0].Page != 2 || hits[0].Snippet != "Euler identity" {
		t.Fatalf("unexpected search hits: %+v", hits)
	}

	if _, err := slot.search(ctx, script, "/tmp/doc.pdf", "bad-query", 50); err == nil || !strings.Contains(err.Error(), "索引损坏") {
		t.Fatalf("expected error from daemon search, got %v", err)
	}
}

func TestWindowsPDFCLIParsing(t *testing.T) {
	count, w, h := parsePDFInfoOutput("Title: Notes\nPages:          42\nPage size:      595.28 x 841.89 pts (A4)\n")
	if count != 42 || w < 595 || h < 841 {
		t.Fatalf("unexpected pdfinfo parse: count=%d w=%g h=%g", count, w, h)
	}

	mCount, mw, mh := parseMutoolInfoOutput("Pages: 18\nMediaBox [ 0 0 612 792 ]\n")
	if mCount != 18 || mw != 612 || mh != 792 {
		t.Fatalf("unexpected mutool parse: count=%d w=%g h=%g", mCount, mw, mh)
	}
}
