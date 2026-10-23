# pkg/doctext

Plain-text extraction for uploaded documents, without any external service.

| Format | How | Notes |
|---|---|---|
| PDF | `github.com/ledongthuc/pdf` (BSD-3-Clause), text layer only | Parser panics are recovered; reading is bounded by pages and time. A PDF without a text layer (a scan) returns `ErrNoText`: there is no OCR. |
| CSV | `encoding/csv`, delimiter sniffed (`,` `;` tab `\|`) | Rows become lines with cells joined by ` \| `. |
| XML | `encoding/xml` token reader | Entities are not expanded and DTDs are not read; nesting deeper than 200 is refused. |
| HTML | `golang.org/x/net/html` tokenizer | Scripts, styles and templates are dropped; block elements become line breaks. |
| Text | UTF-8 (BOM allowed) | Binary and non-UTF-8 input is refused. |

`Extract(name, mediaType, data, Limits)` detects the format from the content (`%PDF-`), the media type, then the
file extension. `DefaultLimits`: 5 MiB input, 1 Mi bytes of text (longer text is cut and `Result.Truncated` is set),
200 PDF pages, 20 s. `Chunks` splits text into passages for indexing.

`TODO(spec)`: OCR is not built (`docs/OPEN_QUESTIONS.md`).

## Tests

`go test ./pkg/doctext` (unit tests; the PDF fixtures are generated in the tests).
