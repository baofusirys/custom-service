package export

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestColumnName(t *testing.T) {
	cases := map[int]string{0: "A", 3: "D", 25: "Z", 26: "AA", 27: "AB", 701: "ZZ", 702: "AAA"}
	for in, want := range cases {
		if got := ColumnName(in); got != want {
			t.Fatalf("ColumnName(%d)=%s want %s", in, got, want)
		}
	}
}

func TestSanitizeAndEscape(t *testing.T) {
	in := "a\x00b\x07c<&>\"'\t\n中文"
	got := xmlEscape(in)
	if strings.ContainsAny(got, "\x00\x07<>") {
		t.Fatalf("unsafe xml: %q", got)
	}
	if !strings.Contains(got, "&lt;&amp;&gt;&quot;&apos;") || !strings.Contains(got, "中文") {
		t.Fatalf("escape wrong: %q", got)
	}
	long := strings.Repeat("字", MaxCellRunes+10)
	if n := len([]rune(SanitizeXMLText(long))); n != MaxCellRunes+1 {
		t.Fatalf("truncate to %d runes, got %d", MaxCellRunes+1, n)
	}
}

func TestWriteXLSXIsValidZipWithInlineStrings(t *testing.T) {
	var buf bytes.Buffer
	err := WriteXLSX(&buf, "聊天记录/测试", []string{"时间", "客户", "发送方", "内容"}, []float64{20, 16, 8, 60},
		func(yield func([]string) error) error {
			if err := yield([]string{"2026-10-14 10:00:00", "王先生", "访客", "=HYPERLINK(\"http://evil\")"}); err != nil {
				return err
			}
			return yield([]string{"2026-10-14 10:00:01", "王先生", "客服", "<script>x</script>"})
		})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(b)
	}
	for _, name := range []string{"[Content_Types].xml", "_rels/.rels", "xl/workbook.xml", "xl/_rels/workbook.xml.rels", "xl/worksheets/sheet1.xml"} {
		if _, ok := files[name]; !ok {
			t.Fatalf("missing part %s", name)
		}
	}
	sheet := files["xl/worksheets/sheet1.xml"]
	if strings.Contains(sheet, "<f>") || strings.Count(sheet, `t="inlineStr"`) != 12 {
		t.Fatalf("cells must be inline strings without formulas: %s", sheet)
	}
	if !strings.Contains(sheet, "&lt;script&gt;") || !strings.Contains(sheet, `<row r="3">`) {
		t.Fatalf("content not escaped or rows missing")
	}
	if strings.Contains(files["xl/workbook.xml"], "聊天记录/测试") {
		t.Fatalf("sheet name must drop '/'")
	}
}

func TestSafeFilenameAndDisposition(t *testing.T) {
	if got := SafeFilename("../a\\b:c*?\"<>|\x00名.xlsx"); strings.ContainsAny(got, "/\\:*?\"<>|\x00") {
		t.Fatalf("unsafe filename %q", got)
	}
	if SafeFilename("   ") != "export" {
		t.Fatal("empty name fallback")
	}
	cd := ContentDisposition("聊天记录 王先生.xlsx", "chat-export.xlsx")
	tail := cd[strings.Index(cd, "UTF-8''"):]
	if !strings.HasPrefix(cd, "attachment; filename=\"chat-export.xlsx\"; filename*=UTF-8''") || strings.ContainsAny(tail, " \"") {
		t.Fatalf("bad disposition %q", cd)
	}
}
