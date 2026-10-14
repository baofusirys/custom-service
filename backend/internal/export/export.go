// Package export 聊天记录导出：标准 xlsx（仅用标准库 archive/zip 流式写出，内存占用与行数无关）与 UTF-8 纯文本。
// 单元格一律使用内联字符串（t="inlineStr"），不会被电子表格当作公式执行，杜绝公式注入。
package export

import (
	"archive/zip"
	"bufio"
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode/utf8"
)

// MaxCellRunes Excel 单元格最多 32767 个字符，超出截断。
const MaxCellRunes = 32000

// SanitizeXMLText 删除 XML 1.0 不允许的控制字符，并按字符数截断。
func SanitizeXMLText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	n := 0
	for _, r := range s {
		if r == utf8.RuneError {
			continue
		}
		if r == '\t' || r == '\n' || r == '\r' || (r >= 0x20 && r <= 0xD7FF) || (r >= 0xE000 && r <= 0xFFFD) || (r >= 0x10000 && r <= 0x10FFFF) {
			if n >= MaxCellRunes {
				b.WriteString("…")
				break
			}
			b.WriteRune(r)
			n++
		}
	}
	return b.String()
}

var xmlReplacer = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;")

func xmlEscape(s string) string { return xmlReplacer.Replace(SanitizeXMLText(s)) }

// ColumnName 0 → A，25 → Z，26 → AA。
func ColumnName(i int) string {
	name := ""
	for i >= 0 {
		name = string(rune('A'+i%26)) + name
		i = i/26 - 1
	}
	return name
}

const contentTypes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`

const rootRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`

const workbookRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`

const workbookHead = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="`

const workbookTail = `" sheetId="1" r:id="rId1"/></sheets></workbook>`

const sheetHead = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews>`

var sheetNameReplacer = strings.NewReplacer("\\", "", "/", "", "?", "", "*", "", "[", "", "]", "", ":", "")

// WriteXLSX 流式写出单工作表 xlsx。widths 为各列宽度（字符数，可为空）。
func WriteXLSX(w io.Writer, sheetName string, header []string, widths []float64, rows func(yield func([]string) error) error) error {
	zw := zip.NewWriter(w)
	put := func(name, body string) error {
		f, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = io.WriteString(f, body)
		return err
	}
	if err := put("[Content_Types].xml", contentTypes); err != nil {
		return err
	}
	if err := put("_rels/.rels", rootRels); err != nil {
		return err
	}
	if err := put("xl/_rels/workbook.xml.rels", workbookRels); err != nil {
		return err
	}
	name := sheetNameReplacer.Replace(sheetName)
	if utf8.RuneCountInString(name) > 31 {
		name = string([]rune(name)[:31])
	}
	if name == "" {
		name = "Sheet1"
	}
	if err := put("xl/workbook.xml", workbookHead+xmlEscape(name)+workbookTail); err != nil {
		return err
	}
	f, err := zw.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(f, 64*1024)
	bw.WriteString(sheetHead)
	if len(widths) > 0 {
		bw.WriteString("<cols>")
		for i, wd := range widths {
			fmt.Fprintf(bw, `<col min="%d" max="%d" width="%.1f" customWidth="1"/>`, i+1, i+1, wd)
		}
		bw.WriteString("</cols>")
	}
	bw.WriteString("<sheetData>")
	rowNum := 0
	writeRow := func(cells []string) error {
		rowNum++
		fmt.Fprintf(bw, `<row r="%d">`, rowNum)
		for i, v := range cells {
			fmt.Fprintf(bw, `<c r="%s%d" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ColumnName(i), rowNum, xmlEscape(v))
		}
		_, err := bw.WriteString("</row>")
		return err
	}
	if len(header) > 0 {
		if err := writeRow(header); err != nil {
			return err
		}
	}
	if rows != nil {
		if err := rows(writeRow); err != nil {
			return err
		}
	}
	bw.WriteString("</sheetData></worksheet>")
	if err := bw.Flush(); err != nil {
		return err
	}
	return zw.Close()
}

// SafeFilename 去掉文件名中的路径分隔符、保留字符与控制字符，限制长度。
func SafeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			continue
		case strings.ContainsRune("\\/:*?\"<>|", r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(out) > 80 {
		out = string([]rune(out)[:80])
	}
	if out == "" {
		out = "export"
	}
	return out
}

// ContentDisposition 生成兼容各浏览器的下载头：ASCII 兜底名 + RFC 5987 UTF-8 文件名。
func ContentDisposition(filename, asciiFallback string) string {
	return fmt.Sprintf("attachment; filename=\"%s\"; filename*=UTF-8''%s", asciiFallback, url.PathEscape(SafeFilename(filename)))
}
