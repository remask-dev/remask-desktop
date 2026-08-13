package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestO200kTokenizerPreservesTokenIDsAndByteOffsets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokenizer.json")
	data, err := json.Marshal(map[string]any{"model": map[string]any{
		"type": "BPE", "ignore_merges": true, "vocab": map[string]int{"My": 5444, "Ġname": 1308},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	tokenizer, err := loadTokenizer(path, TokenizerSpec{Type: "o200k-base"})
	if err != nil {
		t.Fatal(err)
	}
	windows, err := tokenizer.encode("My name is Alice Smith", 128, 16)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []int64{5444, 1308, 382, 44045, 16627}
	if len(windows) != 1 || !reflect.DeepEqual(windows[0].InputIDs, wantIDs) {
		t.Fatalf("windows = %#v, want ids %v", windows, wantIDs)
	}
	last := windows[0].Tokens[len(windows[0].Tokens)-1]
	if last.EndByte != len("My name is Alice Smith") {
		t.Fatalf("last byte offset = %d", last.EndByte)
	}
}

func TestWordPieceTokenizerPreservesUTF8ByteOffsets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vocab.txt")
	vocab := "[PAD]\n[UNK]\n[CLS]\n[SEP]\njohn\n##son\n北\n京\ncafe\n"
	if err := os.WriteFile(path, []byte(vocab), 0o644); err != nil {
		t.Fatal(err)
	}
	tokenizer, err := loadWordPieceTokenizer(path, TokenizerSpec{
		Type: "bert-wordpiece", LowerCase: true, StripAccents: true, TokenizeChineseChars: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := "Johnson 北京 Café"
	windows, err := tokenizer.encode(text, 8, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(windows) != 1 {
		t.Fatalf("expected one window, got %d", len(windows))
	}
	tokens := windows[0].Tokens
	assertTokenOffset(t, text, tokens[1], "John")
	assertTokenOffset(t, text, tokens[2], "son")
	assertTokenOffset(t, text, tokens[3], "北")
	assertTokenOffset(t, text, tokens[4], "京")
	assertTokenOffset(t, text, tokens[5], "Café")
}

func TestWordPieceTokenizerCreatesOverlappingWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vocab.txt")
	if err := os.WriteFile(path, []byte("[UNK]\n[CLS]\n[SEP]\na\nb\nc\nd\ne\nf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tokenizer, err := loadWordPieceTokenizer(path, TokenizerSpec{})
	if err != nil {
		t.Fatal(err)
	}
	windows, err := tokenizer.encode("a b c d e f", 6, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(windows) != 2 {
		t.Fatalf("expected two windows, got %d", len(windows))
	}
	if windows[0].Tokens[3].Index != 2 || windows[1].Tokens[1].Index != 2 {
		t.Fatalf("expected overlap at global token index 2: %#v %#v", windows[0].Tokens, windows[1].Tokens)
	}
}

func assertTokenOffset(t *testing.T, text string, token encodedToken, expected string) {
	t.Helper()
	if actual := text[token.StartByte:token.EndByte]; actual != expected {
		t.Fatalf("offset [%d:%d] produced %q, expected %q", token.StartByte, token.EndByte, actual, expected)
	}
}
