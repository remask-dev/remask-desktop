package audit

import (
	"reflect"
	"testing"
)

func TestSortFieldsUsesNaturalJSONPointerOrder(t *testing.T) {
	fields := []Field{
		{Path: "/messages/10/content"},
		{Path: "/system"},
		{Path: "/messages/2/content"},
		{Path: "/messages/1/content"},
	}

	sortFields(fields)
	got := make([]string, len(fields))
	for index, field := range fields {
		got[index] = field.Path
	}
	want := []string{
		"/messages/1/content",
		"/messages/2/content",
		"/messages/10/content",
		"/system",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}
}
