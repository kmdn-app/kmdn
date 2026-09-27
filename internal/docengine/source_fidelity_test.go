package docengine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSharedSourceHostRoundTrip(t *testing.T) {
	e, err := New(Options{Runtimes: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, edited := range []string{
		"Title\n=====\n\n+ item\n",
		"\ufeffTitle\r\n=====\r\n\r\n\r\n+ item",
		"\ufeff\r\n\r\n",
	} {
		t.Run(edited, func(t *testing.T) {
			state, sourceMap, err := e.YFromMarkdown(ctx, "# Title\n\n* item\n", 7)
			if err != nil {
				t.Fatal(err)
			}
			update, err := e.YApplyMarkdown(ctx, state, edited, 8)
			if err != nil || len(update) <= 2 {
				t.Fatalf("source edit produced no update: %v", err)
			}
			state, err = e.YMerge(ctx, [][]byte{state, update})
			if err != nil {
				t.Fatal(err)
			}
			for _, legacyMap := range []string{sourceMap, ""} {
				got, err := e.YMaterialize(ctx, state, legacyMap)
				if err != nil || got != edited {
					t.Fatalf("materialize shared source: %v\nwant %q\ngot  %q", err, edited, got)
				}
			}
			noop, err := e.YApplyMarkdown(ctx, state, edited, 8)
			if err != nil || len(noop) > 2 {
				t.Fatalf("reapplying source must not grow state: %v, %d bytes", err, len(noop))
			}
		})
	}
}

func TestSharedSourceHostConcurrentFormatting(t *testing.T) {
	e, err := New(Options{Runtimes: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	original := "# First\n\n**Same**\n\n**Same**\n\n## Last\n"
	state, sm, err := e.YFromMarkdown(ctx, original, 7)
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.YApplyMarkdown(ctx, state, strings.Replace(original, "# First", "First\n=====", 1), 40)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.YApplyMarkdown(ctx, state, strings.Replace(original, "## Last", "Last\n----", 1), 41)
	if err != nil {
		t.Fatal(err)
	}
	want := "First\n=====\n\n**Same**\n\n**Same**\n\nLast\n----\n"
	for _, updates := range [][][]byte{{state, a, b}, {state, b, a}} {
		merged, err := e.YMerge(ctx, updates)
		if err != nil {
			t.Fatal(err)
		}
		got, err := e.YMaterialize(ctx, merged, sm)
		if err != nil || got != want {
			t.Fatalf("independent source edits lost: %v\nwant %q\ngot  %q", err, want, got)
		}
		counts, err := e.YContributions(ctx, merged)
		if err != nil || counts[40] != 0 || counts[41] != 0 {
			t.Fatalf("metadata counted as semantic content: %v, %v", counts, err)
		}
	}
}

func TestSharedSourceHostBounds(t *testing.T) {
	writer, err := New(Options{Runtimes: 1, MaxInputBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := New(Options{Runtimes: 1, MaxInputBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, text := range []string{strings.Repeat("\n", 70) + "small\n", strings.Repeat("漢", 25) + "\n"} {
		state, sm, err := writer.YFromMarkdown(ctx, text, 7)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reader.YMaterialize(ctx, state, sm); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("configured metadata limit ignored: %v", err)
		}
		if _, err := reader.YApplyMarkdown(ctx, state, "small\n", 8); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("edit parsed oversized metadata: %v", err)
		}
	}
	state, sm, err := writer.YFromMarkdown(ctx, "Small\n", 7)
	if err != nil {
		t.Fatal(err)
	}
	invalid, err := writer.YSetMapEntry(ctx, state, 8, "source", "unknown", `{"offset":0,"hash":"forged","nested":{"text":"injected"}}`)
	if err != nil {
		t.Fatal(err)
	}
	state, err = writer.YMerge(ctx, [][]byte{state, invalid})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.YMaterialize(ctx, state, sm); err == nil || !strings.Contains(err.Error(), "invalid authored source metadata") {
		t.Fatalf("malformed client metadata was trusted: %v", err)
	}
}
