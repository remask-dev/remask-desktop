package model

import (
	"reflect"
	"testing"
)

func TestDecodeEntitiesMergesBIOAndNormalizesTypes(t *testing.T) {
	text := "John Smith called"
	predictions := []tokenPrediction{
		{Token: encodedToken{StartByte: 0, EndByte: 4}, Label: "B-FIRSTNAME", Confidence: 0.9},
		{Token: encodedToken{StartByte: 5, EndByte: 10}, Label: "I-FIRSTNAME", Confidence: 0.8},
		{Token: encodedToken{StartByte: 11, EndByte: 17}, Label: "O", Confidence: 0.99},
	}
	entities := decodeEntities(text, "model:test", predictions, nil, nil)
	if len(entities) != 1 {
		t.Fatalf("expected one entity, got %#v", entities)
	}
	entity := entities[0]
	if entity.Type != "PERSON" || entity.Text != "John Smith" || entity.StartByte != 0 || entity.EndByte != 10 {
		t.Fatalf("unexpected entity: %#v", entity)
	}
	if entity.Confidence < 0.849 || entity.Confidence > 0.851 {
		t.Fatalf("unexpected confidence: %f", entity.Confidence)
	}
}

func TestMergeWindowPredictionsUsesHigherConfidence(t *testing.T) {
	first := []tokenPrediction{{Token: encodedToken{Index: 3}, Label: "O", Confidence: 0.6}}
	second := []tokenPrediction{{Token: encodedToken{Index: 3}, Label: "B-EMAIL", Confidence: 0.9}}
	merged := mergeWindowPredictions(first, second)
	if len(merged) != 1 || merged[0].Label != "B-EMAIL" {
		t.Fatalf("unexpected merged prediction: %#v", merged)
	}
}

func TestMinimumConfidenceDropsWeakEntity(t *testing.T) {
	predictions := []tokenPrediction{{Token: encodedToken{StartByte: 0, EndByte: 3}, Label: "B-EMAIL", Confidence: 0.7}}
	entities := decodeEntities("foo", "model:test", predictions, nil, map[string]float64{"EMAIL_ADDRESS": 0.8})
	if len(entities) != 0 {
		t.Fatalf("expected weak prediction to be dropped: %#v", entities)
	}
}

func TestUnknownModelLabelIsNotRedacted(t *testing.T) {
	predictions := []tokenPrediction{{Token: encodedToken{StartByte: 0, EndByte: 3}, Label: "B-CURRENCYSYMBOL", Confidence: 0.99}}
	entities := decodeEntities("张", "model:test", predictions, nil, nil)
	if len(entities) != 0 {
		t.Fatalf("expected unsupported label to be ignored: %#v", entities)
	}
}

func TestCanonicalEntityTypeCoversCommonPIIModelLabels(t *testing.T) {
	tests := map[string]string{
		"GIVEN_NAME": "PERSON", "contact.person_name": "PERSON", "EMAIL": "EMAIL_ADDRESS", "contact.email": "EMAIL_ADDRESS",
		"PHONE": "PHONE_NUMBER", "contact.postal_code": "ADDRESS", "TAX_ID": "ACCOUNT_NUMBER", "DRIVERS_LICENSE": "ACCOUNT_NUMBER",
		"BANK_ACCOUNT": "ACCOUNT_NUMBER", "CARD_NUMBER": "ACCOUNT_NUMBER", "CVC": "ACCOUNT_NUMBER", "DATE_OF_BIRTH": "PRIVATE_DATE",
		"credential.api_key": "SECRET", "IP_ADDRESS": "", "GPS_COORDINATES": "ADDRESS", "ORGANIZATION": "",
	}
	for raw, want := range tests {
		if got := CanonicalEntityType(raw); got != want {
			t.Errorf("CanonicalEntityType(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestViterbiBIOESEnforcesCompleteSpan(t *testing.T) {
	labels := []string{"O", "B-private_person", "I-private_person", "E-private_person", "S-private_person"}
	logits := []float32{
		0, 10, 0, 0, 0,
		0, 0, 9, 8, 0,
		0, 0, 10, 1, 0,
	}
	path, err := viterbiBIOES(logits, 3, labels)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3}
	if !reflect.DeepEqual(path, want) {
		t.Fatalf("path = %v, want %v", path, want)
	}
}

func TestDecodeEntitiesPreservesWhitespaceOutsideSpan(t *testing.T) {
	text := "Hello Alice Smith"
	predictions := []tokenPrediction{
		{Token: encodedToken{StartByte: 5, EndByte: 11}, Label: "B-private_person", Confidence: 0.9},
		{Token: encodedToken{StartByte: 11, EndByte: 17}, Label: "E-private_person", Confidence: 0.9},
	}
	entities := decodeEntities(text, "model:test", predictions, nil, nil)
	if len(entities) != 1 || entities[0].Text != "Alice Smith" || entities[0].StartByte != 6 {
		t.Fatalf("unexpected trimmed entity: %#v", entities)
	}
}
