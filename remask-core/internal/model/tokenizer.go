package model

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	gotokenizer "github.com/tiktoken-go/tokenizer"
	"golang.org/x/text/unicode/norm"
)

const maxWordPieceRunes = 100

type encodedToken struct {
	ID        int64
	StartByte int
	EndByte   int
	Index     int
	Special   bool
}

type tokenWindow struct {
	Tokens        []encodedToken
	InputIDs      []int64
	AttentionMask []int64
}

type textTokenizer interface {
	encode(text string, maxTokens, stride int) ([]tokenWindow, error)
}

type wordPieceTokenizer struct {
	vocab                map[string]int64
	unknownID            int64
	classificationID     int64
	separatorID          int64
	lowerCase            bool
	stripAccents         bool
	tokenizeChineseChars bool
}

type mappedRune struct {
	value     rune
	startByte int
	endByte   int
}

type basicToken []mappedRune

func loadWordPieceTokenizer(path string, config TokenizerSpec) (*wordPieceTokenizer, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	vocab := make(map[string]int64)
	scanner := bufio.NewScanner(file)
	var id int64
	for scanner.Scan() {
		token := strings.TrimSuffix(scanner.Text(), "\r")
		if _, exists := vocab[token]; !exists {
			vocab[token] = id
		}
		id++
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(vocab) == 0 {
		return nil, errors.New("tokenizer vocabulary is empty")
	}

	unknown := defaultString(config.UnknownToken, "[UNK]")
	classification := defaultString(config.ClassificationToken, "[CLS]")
	separator := defaultString(config.SeparatorToken, "[SEP]")
	unknownID, ok := vocab[unknown]
	if !ok {
		return nil, fmt.Errorf("unknown token %q is missing from vocabulary", unknown)
	}
	classificationID, ok := vocab[classification]
	if !ok {
		return nil, fmt.Errorf("classification token %q is missing from vocabulary", classification)
	}
	separatorID, ok := vocab[separator]
	if !ok {
		return nil, fmt.Errorf("separator token %q is missing from vocabulary", separator)
	}

	return &wordPieceTokenizer{
		vocab: vocab, unknownID: unknownID, classificationID: classificationID, separatorID: separatorID,
		lowerCase: config.LowerCase, stripAccents: config.StripAccents,
		tokenizeChineseChars: config.TokenizeChineseChars,
	}, nil
}

func (t *wordPieceTokenizer) encode(text string, maxTokens, stride int) ([]tokenWindow, error) {
	if maxTokens < 3 {
		return nil, errors.New("maxTokens must leave room for classification and separator tokens")
	}
	pieces := t.tokenize(text)
	capacity := maxTokens - 2
	if stride >= capacity {
		return nil, errors.New("stride must be less than the model content capacity")
	}
	if len(pieces) == 0 {
		return []tokenWindow{t.window(nil)}, nil
	}

	step := capacity - stride
	windows := make([]tokenWindow, 0, (len(pieces)+step-1)/step)
	for start := 0; start < len(pieces); start += step {
		end := start + capacity
		if end > len(pieces) {
			end = len(pieces)
		}
		content := append([]encodedToken(nil), pieces[start:end]...)
		windows = append(windows, t.window(content))
		if end == len(pieces) {
			break
		}
	}
	return windows, nil
}

type o200kTokenizer struct {
	codec gotokenizer.Codec
}

func loadTokenizer(path string, config TokenizerSpec) (textTokenizer, error) {
	switch config.Type {
	case "", "bert-wordpiece":
		return loadWordPieceTokenizer(path, config)
	case "o200k-base":
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var tokenizerFile struct {
			Model struct {
				Type         string           `json:"type"`
				IgnoreMerges bool             `json:"ignore_merges"`
				Vocab        map[string]int64 `json:"vocab"`
			} `json:"model"`
		}
		if err := json.Unmarshal(data, &tokenizerFile); err != nil {
			return nil, fmt.Errorf("parse o200k tokenizer: %w", err)
		}
		if tokenizerFile.Model.Type != "BPE" || !tokenizerFile.Model.IgnoreMerges ||
			tokenizerFile.Model.Vocab["My"] != 5444 || tokenizerFile.Model.Vocab["Ġname"] != 1308 {
			return nil, errors.New("tokenizer.json is not compatible with o200k_base")
		}
		codec, err := gotokenizer.Get(gotokenizer.O200kBase)
		if err != nil {
			return nil, err
		}
		return &o200kTokenizer{codec: codec}, nil
	default:
		return nil, fmt.Errorf("unsupported tokenizer type %q", config.Type)
	}
}

func (t *o200kTokenizer) encode(text string, maxTokens, stride int) ([]tokenWindow, error) {
	if maxTokens < 1 {
		return nil, errors.New("maxTokens must be positive")
	}
	if stride < 0 || stride >= maxTokens {
		return nil, errors.New("stride must be non-negative and less than maxTokens")
	}
	ids, rawTokens, err := t.codec.Encode(text)
	if err != nil {
		return nil, err
	}
	tokens := make([]encodedToken, 0, len(ids))
	byteOffset := 0
	for index, id := range ids {
		end := byteOffset + len(rawTokens[index])
		tokens = append(tokens, encodedToken{ID: int64(id), StartByte: byteOffset, EndByte: end, Index: index})
		byteOffset = end
	}
	if byteOffset != len(text) {
		return nil, fmt.Errorf("o200k tokenizer consumed %d of %d input bytes", byteOffset, len(text))
	}
	if len(tokens) == 0 {
		return []tokenWindow{{}}, nil
	}
	step := maxTokens - stride
	windows := make([]tokenWindow, 0, (len(tokens)+step-1)/step)
	for start := 0; start < len(tokens); start += step {
		end := start + maxTokens
		if end > len(tokens) {
			end = len(tokens)
		}
		content := append([]encodedToken(nil), tokens[start:end]...)
		inputIDs := make([]int64, len(content))
		attentionMask := make([]int64, len(content))
		for index, token := range content {
			inputIDs[index] = token.ID
			attentionMask[index] = 1
		}
		windows = append(windows, tokenWindow{Tokens: content, InputIDs: inputIDs, AttentionMask: attentionMask})
		if end == len(tokens) {
			break
		}
	}
	return windows, nil
}

func (t *wordPieceTokenizer) window(content []encodedToken) tokenWindow {
	tokens := make([]encodedToken, 0, len(content)+2)
	tokens = append(tokens, encodedToken{ID: t.classificationID, Index: -1, Special: true})
	tokens = append(tokens, content...)
	tokens = append(tokens, encodedToken{ID: t.separatorID, Index: -1, Special: true})
	ids := make([]int64, len(tokens))
	mask := make([]int64, len(tokens))
	for index, token := range tokens {
		ids[index] = token.ID
		mask[index] = 1
	}
	return tokenWindow{Tokens: tokens, InputIDs: ids, AttentionMask: mask}
}

func (t *wordPieceTokenizer) tokenize(text string) []encodedToken {
	basic := t.basicTokenize(text)
	result := make([]encodedToken, 0, len(basic))
	for _, token := range basic {
		result = append(result, t.wordPiece(token)...)
	}
	for index := range result {
		result[index].Index = index
	}
	return result
}

func (t *wordPieceTokenizer) basicTokenize(text string) []basicToken {
	var result []basicToken
	var current basicToken
	flush := func() {
		if len(current) > 0 {
			result = append(result, current)
			current = nil
		}
	}

	for byteIndex, value := range text {
		endByte := byteIndex + utf8.RuneLen(value)
		if isControl(value) {
			continue
		}
		if unicode.IsSpace(value) {
			flush()
			continue
		}

		mapped := t.normalizeRune(value, byteIndex, endByte)
		if len(mapped) == 0 {
			continue
		}
		if (t.tokenizeChineseChars && isChinese(value)) || isPunctuation(value) {
			flush()
			result = append(result, basicToken(mapped))
			continue
		}
		current = append(current, mapped...)
	}
	flush()
	return result
}

func (t *wordPieceTokenizer) normalizeRune(value rune, startByte, endByte int) []mappedRune {
	if t.lowerCase {
		value = unicode.ToLower(value)
	}
	normalized := string(value)
	if t.stripAccents {
		normalized = norm.NFD.String(normalized)
	}
	result := make([]mappedRune, 0, len(normalized))
	for _, candidate := range normalized {
		if t.stripAccents && unicode.Is(unicode.Mn, candidate) {
			continue
		}
		result = append(result, mappedRune{value: candidate, startByte: startByte, endByte: endByte})
	}
	return result
}

func (t *wordPieceTokenizer) wordPiece(token basicToken) []encodedToken {
	if len(token) > maxWordPieceRunes {
		return []encodedToken{{ID: t.unknownID, StartByte: token[0].startByte, EndByte: token[len(token)-1].endByte}}
	}
	result := make([]encodedToken, 0, len(token))
	for start := 0; start < len(token); {
		matchedEnd := -1
		matchedID := int64(0)
		for end := len(token); end > start; end-- {
			piece := mappedRunesString(token[start:end])
			if start > 0 {
				piece = "##" + piece
			}
			if id, ok := t.vocab[piece]; ok {
				matchedEnd = end
				matchedID = id
				break
			}
		}
		if matchedEnd < 0 {
			return []encodedToken{{ID: t.unknownID, StartByte: token[0].startByte, EndByte: token[len(token)-1].endByte}}
		}
		result = append(result, encodedToken{
			ID: matchedID, StartByte: token[start].startByte, EndByte: token[matchedEnd-1].endByte,
		})
		start = matchedEnd
	}
	return result
}

func mappedRunesString(values []mappedRune) string {
	var builder strings.Builder
	for _, value := range values {
		builder.WriteRune(value.value)
	}
	return builder.String()
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func isControl(value rune) bool {
	return value != '\t' && value != '\n' && value != '\r' && unicode.IsControl(value)
}

func isPunctuation(value rune) bool {
	if value >= 33 && value <= 126 {
		return (value >= 33 && value <= 47) || (value >= 58 && value <= 64) ||
			(value >= 91 && value <= 96) || (value >= 123 && value <= 126)
	}
	return unicode.IsPunct(value)
}

func isChinese(value rune) bool {
	return (value >= 0x4E00 && value <= 0x9FFF) ||
		(value >= 0x3400 && value <= 0x4DBF) ||
		(value >= 0x20000 && value <= 0x2A6DF) ||
		(value >= 0x2A700 && value <= 0x2B73F) ||
		(value >= 0x2B740 && value <= 0x2B81F) ||
		(value >= 0x2B820 && value <= 0x2CEAF) ||
		(value >= 0xF900 && value <= 0xFAFF) ||
		(value >= 0x2F800 && value <= 0x2FA1F)
}
