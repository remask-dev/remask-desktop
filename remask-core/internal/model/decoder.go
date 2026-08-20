package model

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/remask-dev/remask-core/internal/pii"
)

type tokenPrediction struct {
	Token      encodedToken
	Label      string
	Confidence float64
}

func loadLabels(path string, manifestLabels map[string]string) ([]string, error) {
	labels := manifestLabels
	if len(labels) == 0 {
		loaded, err := readLabelMap(path)
		if err != nil {
			return nil, err
		}
		labels = loaded
	}
	if len(labels) == 0 {
		return nil, fmt.Errorf("labels are empty")
	}
	result := make([]string, len(labels))
	for rawID, label := range labels {
		id, err := strconv.Atoi(rawID)
		if err != nil || id < 0 || id >= len(result) {
			return nil, fmt.Errorf("invalid label id %q", rawID)
		}
		result[id] = label
	}
	for id, label := range result {
		if label == "" {
			return nil, fmt.Errorf("label id %d is missing", id)
		}
	}
	return result, nil
}

func predictionsFromLogits(window tokenWindow, logits []float32, labels []string, decoderType string) ([]tokenPrediction, error) {
	if len(logits) != len(window.Tokens)*len(labels) {
		return nil, fmt.Errorf("logits contain %d values, expected %d", len(logits), len(window.Tokens)*len(labels))
	}
	labelIDs := make([]int, len(window.Tokens))
	if decoderType == "viterbi-bioes" {
		var err error
		labelIDs, err = viterbiBIOES(logits, len(window.Tokens), labels)
		if err != nil {
			return nil, err
		}
	}
	result := make([]tokenPrediction, 0, len(window.Tokens))
	for tokenIndex, token := range window.Tokens {
		if token.Special {
			continue
		}
		row := logits[tokenIndex*len(labels) : (tokenIndex+1)*len(labels)]
		labelID, confidence := argmaxSoftmax(row)
		if decoderType == "viterbi-bioes" {
			labelID = labelIDs[tokenIndex]
			confidence = softmaxAt(row, labelID)
		}
		result = append(result, tokenPrediction{Token: token, Label: labels[labelID], Confidence: confidence})
	}
	return result, nil
}

func viterbiBIOES(logits []float32, tokenCount int, labels []string) ([]int, error) {
	if tokenCount == 0 {
		return []int{}, nil
	}
	labelCount := len(labels)
	negInf := math.Inf(-1)
	scores := make([]float64, labelCount)
	back := make([][]int, tokenCount)
	back[0] = make([]int, labelCount)
	for state, label := range labels {
		if validBIOESStart(label) {
			scores[state] = float64(logits[state])
		} else {
			scores[state] = negInf
		}
	}
	for token := 1; token < tokenCount; token++ {
		next := make([]float64, labelCount)
		back[token] = make([]int, labelCount)
		for current, currentLabel := range labels {
			bestScore := negInf
			bestPrevious := 0
			for previous, previousLabel := range labels {
				if validBIOESTransition(previousLabel, currentLabel) && scores[previous] > bestScore {
					bestScore = scores[previous]
					bestPrevious = previous
				}
			}
			next[current] = bestScore + float64(logits[token*labelCount+current])
			back[token][current] = bestPrevious
		}
		scores = next
	}
	best := -1
	bestScore := negInf
	for state, label := range labels {
		if validBIOESEnd(label) && scores[state] > bestScore {
			best = state
			bestScore = scores[state]
		}
	}
	if best < 0 {
		return nil, errors.New("BIOES decoder found no valid label path")
	}
	result := make([]int, tokenCount)
	result[tokenCount-1] = best
	for token := tokenCount - 1; token > 0; token-- {
		result[token-1] = back[token][result[token]]
	}
	return result, nil
}

func validBIOESStart(label string) bool {
	prefix, _ := splitEntityLabel(label)
	return prefix == "O" || prefix == "B" || prefix == "S"
}

func validBIOESEnd(label string) bool {
	prefix, _ := splitEntityLabel(label)
	return prefix == "O" || prefix == "E" || prefix == "S"
}

func validBIOESTransition(previous, current string) bool {
	previousPrefix, previousType := splitEntityLabel(previous)
	currentPrefix, currentType := splitEntityLabel(current)
	switch previousPrefix {
	case "O", "E", "S":
		return currentPrefix == "O" || currentPrefix == "B" || currentPrefix == "S"
	case "B", "I":
		return previousType == currentType && (currentPrefix == "I" || currentPrefix == "E")
	default:
		return false
	}
}

func mergeWindowPredictions(windows ...[]tokenPrediction) []tokenPrediction {
	byIndex := make(map[int]tokenPrediction)
	maxIndex := -1
	for _, window := range windows {
		for _, prediction := range window {
			index := prediction.Token.Index
			if previous, ok := byIndex[index]; !ok || prediction.Confidence > previous.Confidence {
				byIndex[index] = prediction
			}
			if index > maxIndex {
				maxIndex = index
			}
		}
	}
	result := make([]tokenPrediction, 0, maxIndex+1)
	for index := 0; index <= maxIndex; index++ {
		if prediction, ok := byIndex[index]; ok {
			result = append(result, prediction)
		}
	}
	return result
}

func decodeEntities(text, source string, predictions []tokenPrediction, entityTypes map[string]string, thresholds map[string]float64) []pii.Entity {
	var result []pii.Entity
	var current *pii.Entity
	confidenceTotal := 0.0
	confidenceCount := 0
	closeCurrent := func() {
		if current == nil {
			return
		}
		trimmed := strings.TrimSpace(text[current.StartByte:current.EndByte])
		if trimmed == "" {
			current = nil
			confidenceTotal = 0
			confidenceCount = 0
			return
		}
		original := text[current.StartByte:current.EndByte]
		left := strings.Index(original, trimmed)
		current.StartByte += left
		current.EndByte = current.StartByte + len(trimmed)
		current.Text = text[current.StartByte:current.EndByte]
		current.Confidence = confidenceTotal / float64(confidenceCount)
		result = append(result, *current)
		current = nil
		confidenceTotal = 0
		confidenceCount = 0
	}

	for _, prediction := range predictions {
		prefix, rawType := splitEntityLabel(prediction.Label)
		entityType := normalizeEntityType(rawType, entityTypes)
		minimum := thresholdFor(entityType, rawType, thresholds)
		if prefix == "O" || entityType == "" || prediction.Confidence < minimum {
			closeCurrent()
			continue
		}

		continues := current != nil && current.Type == entityType && (prefix == "I" || prefix == "E")
		if !continues {
			closeCurrent()
			current = &pii.Entity{
				Type: entityType, StartByte: prediction.Token.StartByte, EndByte: prediction.Token.EndByte,
				Sources: []string{source},
			}
			confidenceTotal = prediction.Confidence
			confidenceCount = 1
		} else {
			current.EndByte = prediction.Token.EndByte
			confidenceTotal += prediction.Confidence
			confidenceCount++
		}
		if prefix == "S" || prefix == "E" {
			closeCurrent()
		}
	}
	closeCurrent()
	return result
}

func splitEntityLabel(label string) (string, string) {
	label = strings.TrimSpace(label)
	if label == "" || strings.EqualFold(label, "O") {
		return "O", ""
	}
	if len(label) > 2 && label[1] == '-' {
		prefix := strings.ToUpper(label[:1])
		if strings.Contains("BIES", prefix) {
			return prefix, strings.ToUpper(label[2:])
		}
	}
	return "B", strings.ToUpper(label)
}

func normalizeEntityType(raw string, custom map[string]string) string {
	raw = strings.ToUpper(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	if value := custom[raw]; value != "" {
		mapped := strings.ToUpper(value)
		if canonical := canonicalOpenAIType(mapped); canonical != "" {
			return canonical
		}
		// A downloaded manifest may preserve a model's raw label as its
		// mapping value (for example TAX_ID -> TAX_ID). Continue through the
		// alias table instead of treating that as an unknown type.
		raw = mapped
	}
	// Many public NER models namespace labels (for example
	// `contact.email` or `identity.passport`). Keep the namespace for the
	// alias lookup, then also try its final component.
	compact := strings.NewReplacer(".", "_", "/", "_", "-", "_", " ", "_").Replace(raw)
	defaults := map[string]string{
		"ACCOUNT_NUMBER": "ACCOUNT_NUMBER", "PRIVATE_ADDRESS": "ADDRESS", "PRIVATE_DATE": "PRIVATE_DATE",
		"PRIVATE_EMAIL": "EMAIL_ADDRESS", "PRIVATE_PERSON": "PERSON", "PRIVATE_PHONE": "PHONE_NUMBER",
		"PRIVATE_URL": "URL", "SECRET": "SECRET",
		"PERSON": "PERSON", "GIVEN_NAME": "PERSON", "SURNAME": "PERSON", "FIRST_NAME": "PERSON", "LAST_NAME": "PERSON", "MIDDLE_NAME": "PERSON", "PATIENT": "PERSON",
		"PHONENUMBER": "PHONE_NUMBER", "PHONE": "PHONE_NUMBER", "TELEPHONENUMBER": "PHONE_NUMBER", "PHONEIMEI": "DEVICE_ID",
		"EMAIL": "EMAIL_ADDRESS", "EMAILADDRESS": "EMAIL_ADDRESS",
		"FIRSTNAME": "PERSON", "MIDDLENAME": "PERSON", "LASTNAME": "PERSON", "PREFIX": "PERSON", "NAME": "PERSON",
		"COMPANYNAME": "ORGANIZATION", "COMPANY": "ORGANIZATION", "ORGANIZATION": "ORGANIZATION",
		"STREET": "ADDRESS", "CITY": "ADDRESS", "STATE": "ADDRESS", "COUNTY": "ADDRESS", "BUILDINGNUMBER": "ADDRESS", "ZIPCODE": "ADDRESS", "ZIP_CODE": "ADDRESS", "POSTCODE": "ADDRESS", "POSTAL_CODE": "ADDRESS", "ADDRESS": "ADDRESS", "SECONDARYADDRESS": "ADDRESS", "SECONDARY_ADDRESS": "ADDRESS", "NEARBYGPSCOORDINATE": "LOCATION",
		"CREDITCARDNUMBER": "BANK_CARD", "CREDIT_CARD_NUMBER": "BANK_CARD", "CREDITCARD": "BANK_CARD", "CARD_NUMBER": "BANK_CARD", "CREDITCARDCVV": "BANK_CARD_SECURITY_CODE", "CVV": "BANK_CARD_SECURITY_CODE", "CVC": "BANK_CARD_SECURITY_CODE", "CARD_EXPIRY": "BANK_CARD_EXPIRY", "IBAN": "BANK_ACCOUNT", "BANKACCOUNT": "BANK_ACCOUNT", "BANK_ACCOUNT": "ACCOUNT_NUMBER", "ACCOUNTNUMBER": "BANK_ACCOUNT", "ROUTING_NUMBER": "BANK_ACCOUNT", "ACCOUNTNAME": "BANK_ACCOUNT_NAME", "BIC": "BANK_IDENTIFIER", "SWIFT_BIC": "BANK_IDENTIFIER", "PIN": "PIN", "PASSWORD": "PASSWORD", "API_KEY": "SECRET", "PRIVATE_KEY": "SECRET", "JWT": "SECRET", "CONNECTION_STRING": "SECRET", "LOGIN_CREDENTIALS": "SECRET",
		"IP": "IP_ADDRESS", "IPV4": "IP_ADDRESS", "IPV6": "IP_ADDRESS",
		"SSN": "NATIONAL_ID", "SOCIALNUM": "NATIONAL_ID", "SOCIAL_NUM": "NATIONAL_ID", "RRN": "NATIONAL_ID", "FRN": "NATIONAL_ID", "GOVERNMENT_ID": "NATIONAL_ID", "GENERIC_ID": "NATIONAL_ID", "TAX_ID": "NATIONAL_ID", "TAXNUM": "NATIONAL_ID", "IDCARD": "NATIONAL_ID", "IDCARDNUM": "NATIONAL_ID", "PASSPORT": "PASSPORT_NUMBER", "PASSPORT_NUMBER": "PASSPORT_NUMBER", "DRIVER_LICENSE": "DRIVER_LICENSE", "DRIVERS_LICENSE": "DRIVER_LICENSE", "DRIVERLICENSENUM": "DRIVER_LICENSE", "USERNAME": "USERNAME", "USER_ID": "USERNAME",
		"VEHICLEVIN": "VEHICLE_ID", "VEHICLEVRM": "VEHICLE_ID", "MAC": "MAC_ADDRESS", "USERAGENT": "USER_AGENT",
		"BITCOINADDRESS": "CRYPTO_ADDRESS", "BITCOIN_ADDRESS": "CRYPTO_ADDRESS", "LITECOINADDRESS": "CRYPTO_ADDRESS", "LITECOIN_ADDRESS": "CRYPTO_ADDRESS", "ETHEREUMADDRESS": "CRYPTO_ADDRESS", "ETHEREUM_ADDRESS": "CRYPTO_ADDRESS", "CRYPTO_WALLET": "CRYPTO_ADDRESS",
		"DOB": "DATE_OF_BIRTH", "DATE_OF_BIRTH": "PRIVATE_DATE", "DATE": "PRIVATE_DATE", "TIME": "PRIVATE_DATE", "URL": "URL", "GPS_COORDINATES": "LOCATION", "IP_ADDRESS": "IP_ADDRESS", "MAC_ADDRESS": "MAC_ADDRESS",
	}
	if value := defaults[raw]; value != "" {
		return canonicalOpenAIType(value)
	}
	if value := defaults[compact]; value != "" {
		return canonicalOpenAIType(value)
	}
	for alias, value := range defaults {
		if strings.HasSuffix(compact, "_"+alias) {
			return canonicalOpenAIType(value)
		}
	}
	if index := strings.LastIndexByte(compact, '_'); index >= 0 {
		if value := defaults[compact[index+1:]]; value != "" {
			return canonicalOpenAIType(value)
		}
	}
	for _, part := range strings.Split(compact, "_") {
		if value := defaults[part]; value != "" {
			return value
		}
	}
	return ""
}

func canonicalOpenAIType(value string) string {
	switch value {
	case "ACCOUNT_NUMBER", "ADDRESS", "PRIVATE_DATE", "EMAIL_ADDRESS", "PERSON", "PHONE_NUMBER", "URL", "SECRET":
		return value
	case "BANK_ACCOUNT", "BANK_CARD", "BANK_CARD_SECURITY_CODE", "BANK_CARD_EXPIRY", "NATIONAL_ID", "PASSPORT_NUMBER", "DRIVER_LICENSE", "VEHICLE_ID", "DEVICE_ID":
		return "ACCOUNT_NUMBER"
	case "LOCATION":
		return "ADDRESS"
	case "DATE_OF_BIRTH":
		return "PRIVATE_DATE"
	default:
		return ""
	}
}

// CanonicalEntityType exposes the same conversion used by the runtime so
// downloaders and manifest tooling can precompute a stable entity map.
func CanonicalEntityType(raw string) string { return normalizeEntityType(raw, nil) }

func thresholdFor(normalized, raw string, thresholds map[string]float64) float64 {
	for _, key := range []string{normalized, raw, "*"} {
		if value, ok := thresholds[key]; ok {
			return value
		}
	}
	return 0
}

func argmaxSoftmax(values []float32) (int, float64) {
	best := 0
	maximum := float64(values[0])
	for index := 1; index < len(values); index++ {
		if value := float64(values[index]); value > maximum {
			best = index
			maximum = value
		}
	}
	sum := 0.0
	for _, value := range values {
		sum += math.Exp(float64(value) - maximum)
	}
	return best, 1 / sum
}

func softmaxAt(values []float32, selected int) float64 {
	maximum := float64(values[0])
	for _, value := range values[1:] {
		if float64(value) > maximum {
			maximum = float64(value)
		}
	}
	sum := 0.0
	for _, value := range values {
		sum += math.Exp(float64(value) - maximum)
	}
	return math.Exp(float64(values[selected])-maximum) / sum
}
