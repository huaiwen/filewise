package workspace

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huaiwen/filewise/internal/documents"
	"github.com/huaiwen/filewise/internal/testdocs"
)

func TestDocumentsSharedReadSearchDiffTasksAndRevocation(t *testing.T) {
	f := newFixture(t, M{})
	before, e := os.ReadFile("../../site/examples/acceptance-en-before.docx")
	require(t, e)
	after, e := os.ReadFile("../../site/examples/acceptance-en-after.docx")
	require(t, e)
	files := map[string][]byte{"acceptance.docx": before, "talk.pptx": testdocs.Slides("Pressure guide", "Second slide", nil), "paper.pdf": testdocs.PDF("Filewise pressure guide"), "mixed.pdf": testdocs.PDF("Pressure excerpt", ""), "bad.pdf": []byte("%PDF-broken")}
	for path, b := range files {
		require(t, os.WriteFile(filepath.Join(f.root, path), b, 0600))
	}
	_, e = f.s.Sync("p", editor)
	require(t, e)
	base := f.base(t)
	for path, b := range files {
		r := f.call(t, "read", M{"path": path, "include_bytes": true})
		raw, e := base64.StdEncoding.DecodeString(str(r["base64"]))
		require(t, e)
		assert(t, documents.Hash(raw) == documents.Hash(b), "binary original changed")
		if path == "bad.pdf" {
			assert(t, r["text"] == nil && r["extraction"].(documents.Info).Status == "failed", "malformed PDF became evidence")
		} else {
			assert(t, len(r["fragments"].([]documents.Fragment)) > 0, "document text missing")
		}
	}
	for _, path := range []string{"acceptance.docx", "talk.pptx", "paper.pdf"} {
		r := f.call(t, "search", M{"query": "pressure", "paths": []any{path}, "mode": "lexical"})
		assert(t, len(list(r["hits"])) > 0, "document did not enter shared retrieval")
	}
	r := f.save(t, "word-update", M{"acceptance.docx": M{"base64": base64.StdEncoding.EncodeToString(after)}})
	id := str(r["version"])
	diff := f.call(t, "diff", M{"before": base, "after": id, "paths": []any{"acceptance.docx"}})
	assert(t, strings.Contains(string(JSON(diff)), "word/document.xml/paragraph:3") && strings.Contains(string(JSON(diff)), "semantic_change"), "document diff lost locator or semantic meta")
	checks := []any{M{"id": "oracle", "object_id": "result", "field": "pressure", "expected": 120}}
	for _, path := range []string{"acceptance.docx", "mixed.pdf", "bad.pdf"} {
		task := f.call(t, "compile", M{"goal": "Check evidence", "paths": []any{path}, "output_checks": checks})
		verified := f.call(t, "verify", M{"task_id": task["task_id"], "phase": "postflight", "result": M{"pressure": 120}})
		if path == "acceptance.docx" {
			assert(t, verified["decision"] == "PASS", "complete Word evidence did not verify")
		} else {
			assert(t, verified["decision"] == "BLOCKED", "incomplete document evidence passed")
		}
	}
	v, e := f.s.Version("p", id, nil, agent)
	require(t, e)
	source := str(v.File("acceptance.docx")["source_id"])
	_, e = f.s.Policy("p", source, []string{"reader", "editor", "reviewer"}, true, reviewer)
	require(t, e)
	_, e = f.s.Dispatch("p", "read", M{"path": "acceptance.docx", "version": id}, agent)
	wantError(t, e, "revoked")
}
func TestWorkbookQualityUsesFullRowsAndNeverFormulaCaches(t *testing.T) {
	header := `<row r="1"><c r="A1" t="inlineStr"><is><t>id</t></is></c><c r="B1" t="inlineStr"><is><t>amount</t></is></c></row>`
	var rows strings.Builder
	rows.WriteString(header)
	for i := 1; i <= 160; i++ {
		value := 1
		if i == 151 {
			value = -1
		}
		fmt.Fprintf(&rows, `<row r="%d"><c r="A%d" t="inlineStr"><is><t>row-%d</t></is></c><c r="B%d"><v>%d</v></c></row>`, i+1, i+1, i, i+1, value)
	}
	contract, e := Contract(dataContract(t))
	require(t, e)
	quality := Quality("data.xlsx", testdocs.Workbook(rows.String(), nil), contract, nil, nil)
	assert(t, quality["decision"] == "BLOCKED" && obj(quality["profile"])["row_count"] == 160, "XLSX full table not checked")
	assert(t, equal(obj(list(quality["tests"])[2])["record_indices"], []any{150}), "late bad XLSX row was missed")
	for _, cell := range []string{`<c r="B2"><f>1</f><v>1</v></c>`, `<c r="B2" t="e"><v>#VALUE!</v></c>`, `<c r="B2" t="b"><v>1</v></c>`, `<c r="B2" s="1"><v>45292</v></c>`} {
		body := testdocs.Workbook(header+`<row r="2"><c r="A2" t="inlineStr"><is><t>one</t></is></c>`+cell+`</row>`, nil)
		quality = Quality("data.xlsx", body, contract, nil, nil)
		assert(t, quality["decision"] == "BLOCKED", "formula/error/bool/date treated as numeric data")
	}
	precise := parsed(t, `{"checks":[{"id":"precision","op":"range","column":"amount","minimum":9007199254740993.11,"maximum":9007199254740993.11}]}`)
	precise, e = Contract(precise)
	require(t, e)
	body := testdocs.Workbook(header+`<row r="2"><c r="A2" t="inlineStr"><is><t>one</t></is></c><c r="B2"><v>9007199254740993.11</v></c></row>`, nil)
	quality = Quality("data.xlsx", body, precise, nil, nil)
	assert(t, quality["decision"] == "PASS", "exact XLSX precision was lost")
}
func TestWatchDocumentsRenameUndoAndFailureIsolation(t *testing.T) {
	s, root, p := watched(t, M{"naming": "auto", "template": "{title}"})
	word, e := os.ReadFile("../../site/examples/acceptance-en-before.docx")
	require(t, e)
	inputs := map[string][]byte{"word.docx": word, "deck.pptx": testdocs.Slides("Slide title", "Second", nil), "document.pdf": testdocs.PDF("PDF title"), "corrupt.pdf": []byte("%PDF-bad")}
	infos := map[string]os.FileInfo{}
	for path, b := range inputs {
		require(t, os.WriteFile(filepath.Join(root, path), b, 0600))
		infos[path], e = os.Stat(filepath.Join(root, path))
		require(t, e)
	}
	require(t, s.WatchScan(p, 100))
	for tick := 104; tick < 140; tick += 4 {
		require(t, s.WatchTick(tick))
	}
	jobs := jobsFor(t, s)
	assert(t, len(jobs) == 5, "document watcher lost a job or looped")
	for _, j := range jobs {
		path := str(j["path"])
		if path == "download.md" {
			continue
		}
		if path == "corrupt.pdf" {
			assert(t, j["status"] == "failed", "bad document was analyzed")
			continue
		}
		assert(t, j["status"] == "done", "valid document did not finish")
		info, e := os.Stat(filepath.Join(root, str(j["output_path"])))
		require(t, e)
		assert(t, os.SameFile(infos[path], info), "document rename lost inode")
		_, e = s.WatchAction(str(j["id"]), "undo", editor)
		require(t, e)
		info, e = os.Stat(filepath.Join(root, path))
		require(t, e)
		assert(t, os.SameFile(infos[path], info), "document undo lost inode")
	}
	_, _, active, e := s.Project(p, editor)
	require(t, e)
	assert(t, active == nil, "document watcher published")
}
