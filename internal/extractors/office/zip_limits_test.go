package office

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func TestReadZipFileRespectsLimit(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if _, err := w.Write([]byte("abcdef")); err != nil {
		t.Fatalf("write entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	_, err = readZipFile(zr, "word/document.xml", 4)
	if err == nil {
		t.Fatalf("expected limit error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "exceeds") {
		t.Fatalf("expected limit error, got %v", err)
	}
}

func TestReadZipFileWithinLimit(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if _, err := w.Write([]byte("abc")); err != nil {
		t.Fatalf("write entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	b, err := readZipFile(zr, "word/document.xml", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(b) != "abc" {
		t.Fatalf("expected abc, got %q", string(b))
	}
}
