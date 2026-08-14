package modeldownload

import "testing"

func TestSelectFilesPreservesExternalDataBasename(t *testing.T) {
	files, err := selectFiles(map[string]bool{
		"onnx/model_q4f16.onnx":      true,
		"onnx/model_q4f16.onnx_data": true,
		"tokenizer.json":             true,
	}, "q4f16")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.key == "model_data" {
			if file.local != "model_q4f16.onnx_data" {
				t.Fatalf("external data local path = %q, want model_q4f16.onnx_data", file.local)
			}
			return
		}
	}
	t.Fatal("selectFiles did not return an external data file")
}
