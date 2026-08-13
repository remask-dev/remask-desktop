package document

import (
	"strings"
	"testing"
)

func TestTransformJSONWildcardSelector(t *testing.T) {
	transformer := NewTransformer()
	body := []byte(`{"messages":[{"content":"alpha"},{"content":[{"type":"text","text":"beta"}]}]}`)

	result, err := transformer.TransformJSON(body, []string{
		"/messages/*/content",
		"/messages/*/content/*/text",
	}, func(value string) (string, error) {
		return strings.ToUpper(value), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"messages":[{"content":"ALPHA"},{"content":[{"text":"BETA","type":"text"}]}]}`
	if string(result) != want {
		t.Fatalf("unexpected result\nwant: %s\n got: %s", want, result)
	}
}
