package extract

type Job struct {
	PresignedURL string
	LocalPath    string
	FileName     string
	MIMEType     string
	FileSize     int64
	Options      map[string]any
}

type Result struct {
	Success   bool              `json:"success"`
	Text      string            `json:"text"`
	Method    string            `json:"method"`
	FileType  string            `json:"fileType"`
	MIMEType  string            `json:"mimeType"`
	Pages     []PageResult      `json:"pages,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	WordCount int               `json:"wordCount"`
	CharCount int               `json:"charCount"`
	Error     *string           `json:"error,omitempty"`
}

type PageResult struct {
	PageNumber int    `json:"pageNumber"`
	Text       string `json:"text"`
	Method     string `json:"method"`
	WordCount  int    `json:"wordCount"`
}

func BuildCounts(text string) (wordCount int, charCount int) {
	charCount = len([]rune(text))
	wordCount = 0
	inWord := false
	for _, r := range text {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			if inWord {
				wordCount++
				inWord = false
			}
			continue
		}
		inWord = true
	}
	if inWord {
		wordCount++
	}
	return
}
