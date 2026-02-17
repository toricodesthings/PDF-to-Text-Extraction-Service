package main

import "testing"

func TestValidatePresignedURLAcceptsAllowedHost(t *testing.T) {
	t.Parallel()

	err := validatePresignedURL(
		"https://abc123.r2.cloudflarestorage.com/user/a/file.pdf?sig=1",
		[]string{".r2.cloudflarestorage.com"},
		false,
	)
	if err != nil {
		t.Fatalf("expected allowed URL, got %v", err)
	}
}

func TestValidatePresignedURLRejectsNonHTTPS(t *testing.T) {
	t.Parallel()

	err := validatePresignedURL(
		"http://abc123.r2.cloudflarestorage.com/user/a/file.pdf",
		[]string{".r2.cloudflarestorage.com"},
		false,
	)
	if err == nil {
		t.Fatalf("expected non-https URL to be rejected")
	}
}

func TestValidatePresignedURLRejectsLocalhostAndPrivateIP(t *testing.T) {
	t.Parallel()

	cases := []string{
		"https://localhost/file.pdf",
		"https://127.0.0.1/file.pdf",
		"https://10.0.0.4/file.pdf",
	}
	for _, c := range cases {
		err := validatePresignedURL(c, []string{".r2.cloudflarestorage.com"}, false)
		if err == nil {
			t.Fatalf("expected URL %q to be rejected", c)
		}
	}
}

func TestValidatePresignedURLRejectsUnapprovedHost(t *testing.T) {
	t.Parallel()

	err := validatePresignedURL(
		"https://example.com/file.pdf",
		[]string{".r2.cloudflarestorage.com"},
		false,
	)
	if err == nil {
		t.Fatalf("expected unapproved host to be rejected")
	}
}

func TestValidatePresignedURLAllowsPrivateLocalWhenEnabled(t *testing.T) {
	t.Parallel()

	cases := []string{
		"http://localhost/file.pdf",
		"http://127.0.0.1/file.pdf",
		"https://10.0.0.4/file.pdf",
	}
	for _, c := range cases {
		err := validatePresignedURL(c, []string{".r2.cloudflarestorage.com"}, true)
		if err != nil {
			t.Fatalf("expected URL %q to be allowed with private flag, got %v", c, err)
		}
	}
}

func TestValidatePresignedURLStillRejectsPublicHTTPWhenEnabled(t *testing.T) {
	t.Parallel()

	err := validatePresignedURL(
		"http://abc123.r2.cloudflarestorage.com/user/a/file.pdf",
		[]string{".r2.cloudflarestorage.com"},
		true,
	)
	if err == nil {
		t.Fatalf("expected public http URL to be rejected even with private flag")
	}
}
