package document

import (
	"reflect"
	"testing"
)

func TestTransformJSONMatchesVisitsObjectWildcardInStableOrder(t *testing.T) {
	transformer := NewTransformer()
	var paths []string

	_, err := transformer.TransformJSONMatches(
		[]byte(`{"values":{"z":"last","a":"first","m":"middle"}}`),
		[]string{"/values/*"},
		func(match TextMatch) (string, error) {
			paths = append(paths, match.Path)
			return match.Value, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/values/a", "/values/m", "/values/z"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
}
