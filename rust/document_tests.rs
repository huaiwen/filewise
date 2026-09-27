use super::*;
use filewise::worker;
use std::io::{Cursor, Write};
const W: &str = "http://schemas.openxmlformats.org/wordprocessingml/2006/main";
const A: &str = "http://schemas.openxmlformats.org/drawingml/2006/main";
const P: &str = "http://schemas.openxmlformats.org/presentationml/2006/main";
const S: &str = "http://schemas.openxmlformats.org/spreadsheetml/2006/main";
const R: &str = "http://schemas.openxmlformats.org/officeDocument/2006/relationships";
const REL: &str = "http://schemas.openxmlformats.org/package/2006/relationships";
fn init() {
    worker::executable(PathBuf::from(env!("CARGO_BIN_EXE_filewise"))).unwrap();
}
fn archive(parts: &[(&str, String)]) -> Vec<u8> {
    let mut zip = zip::ZipWriter::new(Cursor::new(vec![]));
    for (name, text) in parts {
        zip.start_file(
            *name,
            zip::write::SimpleFileOptions::default()
                .compression_method(zip::CompressionMethod::Deflated),
        )
        .unwrap();
        zip.write_all(text.as_bytes()).unwrap();
    }
    zip.finish().unwrap().into_inner()
}
fn root(part: &str) -> String {
    format!(
        r#"<Relationships xmlns="{REL}"><Relationship Id="root" Type="{R}/officeDocument" Target="{part}"/></Relationships>"#
    )
}
pub(super) fn word(title: &str) -> Vec<u8> {
    archive(&[
        ("_rels/.rels", root("word/document.xml")),
        (
            "word/document.xml",
            format!(
                r#"<w:document xmlns:w="{W}" xmlns:r="{R}"><w:body><w:p><w:r><w:t>{title}</w:t></w:r><w:r><w:t> &amp; 机器故障</w:t></w:r><w:del><w:r><w:delText>DELETED SECRET</w:delText></w:r></w:del></w:p><w:tbl><w:tr><w:tc><w:p><w:r><w:t>Pressure</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>120 kPa</w:t></w:r></w:p></w:tc></w:tr></w:tbl><w:sectPr><w:headerReference r:id="header"/></w:sectPr></w:body></w:document>"#
            ),
        ),
        (
            "word/_rels/document.xml.rels",
            format!(
                r#"<Relationships xmlns="{REL}"><Relationship Id="header" Type="{R}/header" Target="header1.xml"/></Relationships>"#
            ),
        ),
        (
            "word/header1.xml",
            format!(
                r#"<w:hdr xmlns:w="{W}"><w:p><w:r><w:t>Synthetic header</w:t></w:r></w:p></w:hdr>"#
            ),
        ),
    ])
}
pub(super) fn slides() -> Vec<u8> {
    archive(&[
        ("_rels/.rels", root("ppt/presentation.xml")),
        (
            "ppt/presentation.xml",
            format!(
                r#"<p:presentation xmlns:p="{P}" xmlns:r="{R}"><p:sldIdLst><p:sldId id="256" r:id="second"/><p:sldId id="257" r:id="first"/></p:sldIdLst></p:presentation>"#
            ),
        ),
        (
            "ppt/_rels/presentation.xml.rels",
            format!(
                r#"<Relationships xmlns="{REL}"><Relationship Id="first" Type="{R}/slide" Target="slides/slide1.xml"/><Relationship Id="second" Type="{R}/slide" Target="slides/slide2.xml"/></Relationships>"#
            ),
        ),
        (
            "ppt/slides/slide1.xml",
            format!(
                r#"<p:sld xmlns:p="{P}" xmlns:a="{A}"><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>Second in presentation</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>"#
            ),
        ),
        (
            "ppt/slides/slide2.xml",
            format!(
                r#"<p:sld xmlns:p="{P}" xmlns:a="{A}"><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>First slide 机器故障</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>"#
            ),
        ),
        (
            "ppt/slides/_rels/slide2.xml.rels",
            format!(
                r#"<Relationships xmlns="{REL}"><Relationship Id="notes" Type="{R}/notesSlide" Target="../notesSlides/notesSlide1.xml"/></Relationships>"#
            ),
        ),
        (
            "ppt/notesSlides/notesSlide1.xml",
            format!(
                r#"<p:notes xmlns:p="{P}" xmlns:a="{A}"><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>Speaker notes, not a slide</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:notes>"#
            ),
        ),
    ])
}
pub(super) fn sheet(amount: &str, formula: bool) -> Vec<u8> {
    let formula = if formula {
        "<f t=\"array\" ref=\"B2:B3\">1/0</f>"
    } else {
        ""
    };
    archive(&[
        ("_rels/.rels", root("xl/workbook.xml")),
        (
            "xl/workbook.xml",
            format!(
                r#"<workbook xmlns="{S}" xmlns:r="{R}"><workbookPr date1904="0"/><sheets><sheet name="Data" sheetId="3" r:id="data"/><sheet name="日期" sheetId="9" r:id="dates"/></sheets></workbook>"#
            ),
        ),
        (
            "xl/_rels/workbook.xml.rels",
            format!(
                r#"<Relationships xmlns="{REL}"><Relationship Id="data" Type="{R}/worksheet" Target="worksheets/sheet3.xml"/><Relationship Id="dates" Type="{R}/worksheet" Target="worksheets/sheet9.xml"/><Relationship Id="strings" Type="{R}/sharedStrings" Target="sharedStrings.xml"/><Relationship Id="styles" Type="{R}/styles" Target="styles.xml"/></Relationships>"#
            ),
        ),
        (
            "xl/sharedStrings.xml",
            format!(
                r#"<sst xmlns="{S}"><si><t>id</t></si><si><r><t>amo</t></r><r><t>unt</t></r></si><si><t>机器故障</t><rPh><t>excluded pronunciation</t></rPh></si></sst>"#
            ),
        ),
        (
            "xl/styles.xml",
            format!(
                r#"<styleSheet xmlns="{S}"><cellXfs count="2"><xf numFmtId="0"/><xf numFmtId="14"/></cellXfs></styleSheet>"#
            ),
        ),
        (
            "xl/worksheets/sheet3.xml",
            format!(
                r#"<worksheet xmlns="{S}"><dimension ref="A1:XFD1048576"/><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row><row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2">{formula}<v>{amount}</v></c></row><row r="3"><c r="A3" t="inlineStr"><is><t>B</t></is></c><c r="B3"><v>0.10000000000000000001</v></c></row></sheetData></worksheet>"#
            ),
        ),
        (
            "xl/worksheets/sheet9.xml",
            format!(
                r#"<worksheet xmlns="{S}"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>date</t></is></c></row><row r="2"><c r="A2" s="1"><v>60</v></c></row></sheetData></worksheet>"#
            ),
        ),
    ])
}
pub(super) fn pdf(blank: bool, chinese: bool) -> Vec<u8> {
    use lopdf::{
        Document, Object, Stream,
        content::{Content, Operation},
        dictionary,
    };
    let mut doc = Document::with_version("1.5");
    let pages_id = doc.new_object_id();
    let font_id = if chinese {
        let cmap = b"/CIDInit /ProcSet findresource begin 12 dict begin begincmap /CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def /CMapName /Synthetic def /CMapType 2 def 1 begincodespacerange <0000> <FFFF> endcodespacerange 4 beginbfchar <0001> <673A> <0002> <5668> <0003> <6545> <0004> <969C> endbfchar endcmap CMapName currentdict /CMap defineresource pop end end";
        let cmap_id = doc.add_object(Stream::new(dictionary! {}, cmap.to_vec()));
        let descriptor = doc.add_object(dictionary!{"Type"=>"FontDescriptor","FontName"=>"Synthetic","Flags"=>4,"FontBBox"=>vec![0.into(),(-200).into(),1000.into(),1000.into()],"ItalicAngle"=>0,"Ascent"=>880,"Descent"=>-120,"CapHeight"=>880,"StemV"=>80});
        let cid = doc.add_object(dictionary!{"Type"=>"Font", "Subtype"=>"CIDFontType2", "BaseFont"=>"Synthetic", "CIDSystemInfo"=>dictionary!{"Registry"=>Object::string_literal("Adobe"),"Ordering"=>Object::string_literal("Identity"),"Supplement"=>0}, "DW"=>1000,"FontDescriptor"=>descriptor});
        doc.add_object(dictionary!{"Type"=>"Font","Subtype"=>"Type0","BaseFont"=>"Synthetic","Encoding"=>"Identity-H","DescendantFonts"=>vec![Object::Reference(cid)],"ToUnicode"=>cmap_id})
    } else {
        doc.add_object(dictionary! {"Type"=>"Font","Subtype"=>"Type1","BaseFont"=>"Helvetica"})
    };
    let resources = doc.add_object(dictionary! {"Font"=>dictionary!{"F1"=>font_id}});
    let ops = if blank {
        vec![
            Operation::new("re", vec![10.into(), 10.into(), 40.into(), 40.into()]),
            Operation::new("f", vec![]),
        ]
    } else {
        vec![
            Operation::new("BT", vec![]),
            Operation::new("Tf", vec!["F1".into(), 14.into()]),
            Operation::new("Td", vec![50.into(), 700.into()]),
            Operation::new(
                "Tj",
                vec![Object::string_literal(if chinese {
                    vec![0, 1, 0, 2, 0, 3, 0, 4]
                } else {
                    b"Paper title - pressure 120 kPa".to_vec()
                })],
            ),
            Operation::new("ET", vec![]),
        ]
    };
    let stream = doc.add_object(Stream::new(
        dictionary! {},
        Content { operations: ops }.encode().unwrap(),
    ));
    let page = doc.add_object(dictionary! {"Type"=>"Page","Parent"=>pages_id,"Contents"=>stream});
    doc.objects.insert(pages_id,dictionary!{"Type"=>"Pages","Kids"=>vec![Object::Reference(page)],"Count"=>1,"Resources"=>resources,"MediaBox"=>vec![0.into(),0.into(),595.into(),842.into()]}.into());
    let catalog = doc.add_object(dictionary! {"Type"=>"Catalog","Pages"=>pages_id});
    doc.trailer.set("Root", catalog);
    let mut bytes = vec![];
    doc.save_to(&mut bytes).unwrap();
    bytes
}

#[test]
fn documents_word_slides_and_page_text_have_real_locations() {
    init();
    let word = files::extract("REPORT.DOCX", &word("Document title")).unwrap();
    assert_eq!(word.info.status, "text", "{:?}", word.info);
    assert!(
        word.fragments
            .iter()
            .any(|f| f.text == "Document title & 机器故障")
    );
    assert!(
        word.fragments
            .iter()
            .any(|f| f.locator.contains("table:1/row:1/cell:2") && f.text == "120 kPa")
    );
    assert!(
        word.fragments
            .iter()
            .any(|f| f.locator.starts_with("word/header1.xml") && f.text == "Synthetic header")
    );
    assert!(
        !serde_json::to_string(&word)
            .unwrap()
            .contains("DELETED SECRET")
    );
    let ppt = files::extract("slides.pptx", &slides()).unwrap();
    assert_eq!(ppt.info.status, "text", "{:?}", ppt.info);
    assert_eq!(ppt.fragments[0].text, "First slide 机器故障");
    assert!(
        ppt.fragments
            .iter()
            .any(|f| f.locator.starts_with("slide:1/notes/") && f.text.contains("Speaker notes"))
    );
    assert!(
        ppt.fragments
            .iter()
            .any(|f| f.locator.starts_with("slide:2/") && f.text == "Second in presentation")
    );
    for chinese in [false, true] {
        let parsed = files::extract("sample.pdf", &pdf(false, chinese)).unwrap();
        assert_eq!(
            parsed.info.status, "text",
            "Chinese={chinese}: {:?}",
            parsed.info
        );
        assert!(
            parsed
                .fragments
                .iter()
                .any(|f| f.locator.starts_with("page:1/line:")
                    && f.text.contains(if chinese {
                        "机器故障"
                    } else {
                        "pressure 120 kPa"
                    })),
            "{:?}",
            parsed.fragments
        );
    }
}
#[test]
fn documents_xlsx_keeps_precision_formulas_and_date_semantics() {
    init();
    let bytes = sheet("9007199254740993", false);
    let parsed = files::extract("DATA.XLSX", &bytes).unwrap();
    assert_eq!(parsed.info.status, "text", "{:?}", parsed.info);
    assert_eq!(parsed.sheets.len(), 2);
    assert_eq!(parsed.sheets[1].name, "日期");
    assert!(
        parsed
            .fragments
            .iter()
            .any(|f| f.locator == "sheet:1/cell:B2" && f.text == "9007199254740993")
    );
    assert!(
        !serde_json::to_string(&parsed)
            .unwrap()
            .contains("excluded pronunciation")
    );
    let contract:DataContract=serde_json::from_value(json!({"checks":[{"id":"precision","op":"range","column":"amount","maximum":9007199254740992u64}]})).unwrap();
    let report = compute::quality("a.xlsx", &bytes, &contract, None, None);
    assert_eq!(report["decision"], "BLOCKED");
    assert_eq!(report["tests"][0]["record_indices"], json!([0]));
    let formula = files::extract("a.xlsx", &sheet("5", true)).unwrap();
    assert_eq!(
        formula.sheets[0]
            .cells
            .iter()
            .find(|c| c.address == "B2")
            .unwrap()
            .formula
            .as_deref(),
        Some("1/0")
    );
    let report = compute::quality("a.xlsx", &sheet("5", true), &contract, None, None);
    assert_eq!(report["decision"], "BLOCKED");
    assert_eq!(report["tests"][0]["record_indices"], json!([0, 1]));
    assert_eq!(
        formula.sheets[0]
            .cells
            .iter()
            .find(|c| c.address == "B3")
            .unwrap()
            .formula_origin
            .as_deref(),
        Some("B2")
    );
    let mut dates = contract.clone();
    dates.sheet = 2;
    dates.checks[0].column = Some("date".into());
    assert_eq!(
        compute::quality("a.xlsx", &bytes, &dates, None, None)["decision"],
        "BLOCKED"
    );
    assert_eq!(parsed.sheets[1].cells[1].value["excel_serial"], "60");
    assert_eq!(parsed.sheets[1].cells[1].value["date_system"], 1900);
}
#[test]
fn documents_invalid_packages_and_empty_pdfs_do_not_become_evidence() {
    init();
    let empty = files::extract("scan.pdf", &pdf(true, false)).unwrap();
    assert_eq!(empty.info.status, "no_text");
    assert!(empty.fragments.is_empty());
    for (name, bytes) in [
        ("bad.pdf", b"%PDF-1.4\n(This is not page text)".to_vec()),
        ("bad.docx", b"not a zip".to_vec()),
        (
            "dtd.docx",
            archive(&[
                ("_rels/.rels", root("word/document.xml")),
                (
                    "word/document.xml",
                    format!(
                        r#"<!DOCTYPE document [<!ENTITY x SYSTEM "file:///etc/passwd">]><w:document xmlns:w="{W}"><w:body><w:p><w:r><w:t>&x;</w:t></w:r></w:p></w:body></w:document>"#
                    ),
                ),
            ]),
        ),
        ("escape.docx", archive(&[("../escape.xml", "<a/>".into())])),
        (
            "external.docx",
            archive(&[(
                "_rels/.rels",
                format!(
                    r#"<Relationships xmlns="{REL}"><Relationship Id="root" Type="{R}/officeDocument" Target="http://127.0.0.1:1/private" TargetMode="External"/></Relationships>"#
                ),
            )]),
        ),
        (
            "bomb.docx",
            archive(&[
                ("_rels/.rels", root("word/document.xml")),
                (
                    "word/document.xml",
                    format!("<a>{}</a>", "x".repeat(9 * 1024 * 1024)),
                ),
            ]),
        ),
    ] {
        let result = files::extract(name, &bytes).unwrap();
        assert_eq!(result.info.status, "failed", "{name}: {:?}", result.info);
        assert!(result.fragments.is_empty());
    }
    let mut duplicate = archive(&[("same-a.xml", "<a/>".into()), ("same-b.xml", "<b/>".into())]);
    for i in 0..duplicate.len() - 10 {
        if &duplicate[i..i + 10] == b"same-b.xml" {
            duplicate[i + 5] = b'a';
        }
    }
    assert_eq!(
        files::extract("duplicate.docx", &duplicate)
            .unwrap()
            .info
            .status,
        "failed"
    );
    assert_eq!(
        files::extract("fake.txt", b"%PDF-1.4\nnot text")
            .unwrap()
            .info
            .status,
        "unsupported"
    );
    assert_eq!(
        files::extract("legacy.doc", b"legacy bytes")
            .unwrap()
            .info
            .status,
        "unsupported"
    );
}
#[test]
fn documents_shared_search_diff_tasks_and_revocation_use_originals() {
    init();
    let f = Fixture::new(json!({}));
    fs::write(f.root.join("report.docx"), word("Version one")).unwrap();
    fs::write(f.root.join("data.xlsx"), sheet("120", false)).unwrap();
    fs::write(f.root.join("scan.pdf"), pdf(true, false)).unwrap();
    fs::write(f.root.join("broken.pdf"), b"not a PDF").unwrap();
    let mut s = f.open();
    s.sync("p", &editor()).unwrap();
    let old = base(&s);
    let read = s
        .read(
            "p",
            &q(json!({"path":"report.docx","include_bytes":true})),
            &agent(),
        )
        .unwrap();
    assert_eq!(read["extraction"]["status"], "text");
    assert_eq!(
        STANDARD.decode(read["base64"].as_str().unwrap()).unwrap(),
        word("Version one")
    );
    let e = Evidence {
        source_id: read["source_id"].as_str().unwrap().into(),
        locator: read["fragments"][0]["locator"].as_str().unwrap().into(),
        quote: "Version one".into(),
    };
    s.check_evidence("p", &e, &agent()).unwrap();
    let hits = compute::search(
        &mut s,
        "p",
        &q(json!({"query":"机器故障","paths":["report.docx"],"mode":"lexical"})),
        &agent(),
    )
    .unwrap();
    assert!(
        hits["hits"]
            .as_array()
            .unwrap()
            .iter()
            .any(|h| h["kind"] == "content" && h["locator"] == e.locator)
    );
    let task = compute::compile(
        &mut s,
        "p",
        &q(json!({"paths":["scan.pdf"],"goal":"Read scan"})),
        &agent(),
    )
    .unwrap();
    assert_eq!(
        compute::verify(
            &mut s,
            "p",
            &q(json!({"phase":"preflight","task_id":task["task_id"]})),
            &agent()
        )
        .unwrap()["decision"],
        "BLOCKED"
    );
    assert_eq!(
        s.read("p", &q(json!({"path":"broken.pdf"})), &agent())
            .unwrap()["extraction"]["status"],
        "failed"
    );
    fs::write(f.root.join("data.xlsx"), sheet("130", false)).unwrap();
    s.sync("p", &editor()).unwrap();
    let diff = compute::diff(
        &s,
        "p",
        &q(json!({"before":old,"paths":["data.xlsx"]})),
        &agent(),
    )
    .unwrap();
    assert_eq!(diff["changes"][0]["locations"].as_array().unwrap().len(), 1);
    assert_eq!(
        diff["changes"][0]["locations"][0]["locator"],
        "sheet:1/cell:B2"
    );
    assert_eq!(diff["changes"][0]["locations"][0]["before"], "120");
    s.policy(
        "p",
        &e.source_id,
        [Role::Reader, Role::Editor].into(),
        true,
        &reviewer(),
    )
    .unwrap();
    assert!(s.check_evidence("p", &e, &agent()).is_err());
    assert!(
        s.read(
            "p",
            &q(json!({"path":"report.docx","version":old})),
            &agent()
        )
        .is_err()
    );
}
#[test]
fn documents_pdf_encryption_mixed_pages_and_tree_integrity() {
    use lopdf::{EncryptionState, EncryptionVersion, Object, Permissions, Stream, dictionary};
    init();
    for password in ["", "synthetic-reader"] {
        let mut doc = lopdf::Document::load_mem(&pdf(false, false)).unwrap();
        doc.trailer.set(
            "ID",
            vec![
                Object::string_literal("synthetic-id-0001"),
                Object::string_literal("synthetic-id-0001"),
            ],
        );
        let state = EncryptionState::try_from(EncryptionVersion::V2 {
            document: &doc,
            owner_password: "synthetic-owner",
            user_password: password,
            key_length: 128,
            permissions: Permissions::all(),
        })
        .unwrap();
        doc.encrypt(&state).unwrap();
        let mut bytes = vec![];
        doc.save_to(&mut bytes).unwrap();
        let result = files::extract("locked.pdf", &bytes).unwrap();
        assert_eq!(result.info.status, "failed");
        assert!(
            result
                .info
                .error
                .as_deref()
                .unwrap_or_default()
                .contains("Encrypted"),
            "{:?}",
            result.info
        );
    }
    let mut doc = lopdf::Document::load_mem(&pdf(false, false)).unwrap();
    let pages = doc
        .catalog()
        .unwrap()
        .get(b"Pages")
        .unwrap()
        .as_reference()
        .unwrap();
    let stream = doc.add_object(Stream::new(dictionary! {}, b"10 10 40 40 re f".to_vec()));
    let page = doc.add_object(dictionary! {"Type"=>"Page","Parent"=>pages,"Contents"=>stream});
    let tree = doc.get_object_mut(pages).unwrap().as_dict_mut().unwrap();
    tree.get_mut(b"Kids")
        .unwrap()
        .as_array_mut()
        .unwrap()
        .push(page.into());
    tree.set("Count", 2);
    let mut bytes = vec![];
    doc.save_to(&mut bytes).unwrap();
    let result = files::extract("mixed.pdf", &bytes).unwrap();
    assert_eq!(result.info.status, "partial");
    assert!(result.info.notes.contains(&"page:2:no_text".to_owned()));
    assert!(
        result
            .fragments
            .iter()
            .any(|f| f.locator.starts_with("page:1/") && f.text.contains("pressure"))
    );
    doc.get_object_mut(pages)
        .unwrap()
        .as_dict_mut()
        .unwrap()
        .set("Count", 3);
    let mut bytes = vec![];
    doc.save_to(&mut bytes).unwrap();
    assert_eq!(
        files::extract("incomplete.pdf", &bytes)
            .unwrap()
            .info
            .status,
        "failed"
    );
}
#[test]
fn documents_partial_extraction_is_explicit_and_blocks_complete_tasks() {
    init();
    let package = archive(&[
        ("_rels/.rels", root("word/document.xml")),
        (
            "word/document.xml",
            format!(
                r#"<w:document xmlns:w="{W}"><w:body><w:p><w:r><w:t>Readable text</w:t></w:r></w:p></w:body></w:document>"#
            ),
        ),
        ("word/media/image1.png", "image bytes are not OCR".into()),
    ]);
    let extracted = files::extract("partial.docx", &package).unwrap();
    assert_eq!(extracted.info.status, "partial");
    let analysis = filewise::watch::analyze("partial.docx", &package, &Default::default()).unwrap();
    assert_eq!(analysis.coverage, "extractive_text_partial");
    let f = Fixture::new(json!({}));
    fs::write(f.root.join("partial.docx"), package).unwrap();
    let mut s = f.open();
    s.sync("p", &editor()).unwrap();
    let task = compute::compile(
        &mut s,
        "p",
        &q(json!({"paths":["partial.docx"],"goal":"Read all evidence"})),
        &agent(),
    )
    .unwrap();
    assert_eq!(task["context"][0]["extraction"]["status"], "partial");
    assert_eq!(
        compute::verify(
            &mut s,
            "p",
            &q(json!({"phase":"preflight","task_id":task["task_id"]})),
            &agent()
        )
        .unwrap()["decision"],
        "BLOCKED"
    );
}
