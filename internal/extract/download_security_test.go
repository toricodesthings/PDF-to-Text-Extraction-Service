package extract

import "testing"

func TestValidateDownloadURLRejectsNonHTTPS(t *testing.T) {
	if err := validateDownloadURL("http://example.com/file.pdf"); err == nil {
		t.Fatalf("expected non-https URL to be rejected")
	}
}

func TestValidateDownloadURLRejectsLocalAndPrivateHosts(t *testing.T) {
	cases := []string{
		"https://localhost/file.pdf",
		"https://127.0.0.1/file.pdf",
		"https://10.0.0.5/file.pdf",
		"https://192.168.1.5/file.pdf",
	}

	for _, c := range cases {
		if err := validateDownloadURL(c); err == nil {
			t.Fatalf("expected URL %q to be rejected", c)
		}
	}
}

func TestValidateDownloadURLAllowsPublicHTTPS(t *testing.T) {
	if err := validateDownloadURL("https://example.com/file.pdf"); err != nil {
		t.Fatalf("expected public https URL to be allowed, got %v", err)
	}
}

func TestValidateDownloadURLAllowsPrivateLocalWhenEnabled(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_DOWNLOAD_URLS", "1")

	cases := []string{
		"http://localhost/file.pdf",
		"http://127.0.0.1/file.pdf",
		"https://10.0.0.5/file.pdf",
	}
	for _, c := range cases {
		if err := validateDownloadURL(c); err != nil {
			t.Fatalf("expected URL %q to be allowed with private flag, got %v", c, err)
		}
	}
}

func TestValidateDownloadURLRejectsPublicHTTPWhenEnabled(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_DOWNLOAD_URLS", "1")

	if err := validateDownloadURL("http://example.com/file.pdf"); err == nil {
		t.Fatalf("expected public http URL to remain rejected")
	}
}
