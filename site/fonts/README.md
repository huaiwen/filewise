# Self-hosted display fonts

The website requests these files from its own origin. It makes no runtime requests to Google Fonts. Both fonts are distributed under SIL Open Font License 1.1; deploy the two accompanying license files with the binaries.

| File | Family / coverage | Bytes | SHA-256 |
| --- | --- | ---: | --- |
| `barlow-condensed-latin-300.woff2` | Barlow Condensed, weight 300, Google Fonts Latin subset | 14,668 | `d4f7b49067c4494c37f04686b56eeb072854f76ddfb4bc0aad6e0421a2c716c1` |
| `noto-sans-sc-headings-300.woff2` | Noto Sans SC, weight 300, current heading subset | 5,484 | `edfa610491c02f6f061047cc2abb2dc623a0ca841c275f107cc20073378ce5bf` |

## Sources

Barlow Condensed binary:
<https://fonts.gstatic.com/s/barlowcondensed/v13/HTxwL3I-JCGChYJ8VI-L6OO_au7B47rxz3bWuYMBYro.woff2>

Noto Sans SC binary:
<https://fonts.gstatic.com/l/font?kit=k3kCo84MPvpLmixcA63oeAL7Iqp5IZJF9bmaG4HFnYlNbPzT7HF7pXYqSi2DOKoiVBj2Ot7Ub-X2tSlt4hakYMK1P7L7ZADMMu85YrNjprbne16aAUZ8eT7z41Gk2osjMSSrrY1ieVm9pvMqjYIRV93RvnSYigVqgoJlX9s&skey=cf0bb10b5602fdc3&v=v40>

The subset was requested through Google Fonts CSS2 using `family=Noto+Sans+SC:wght@300`, `display=swap`, and the URL-encoded text:

```text
让文件有版本，让Agent有依据。文件在变化，依据要跟得上。你的文件。你的Agent。从本地开始。关于Filewise。
```

Spaces are supplied by the font; other glyphs fall back to the system stack. If headings change, regenerate this subset, verify the WOFF2 response, update its hash and inspect both languages. CSS calls it `Filewise Headings SC` to scope its use to the website; the distributed font binary is unchanged from the Google Fonts response.

License sources (the full notices are retained locally):
- <https://raw.githubusercontent.com/google/fonts/main/ofl/barlowcondensed/OFL.txt> → `BarlowCondensed-OFL.txt`
- <https://raw.githubusercontent.com/google/fonts/main/ofl/notosanssc/OFL.txt> → `NotoSansSC-OFL.txt`

Barlow copyright: The Barlow Project Authors, 2017. Noto Sans SC notice: Adobe, 2014–2021, Reserved Font Name “Source”. Font licenses apply to the fonts; they do not establish a license for the Filewise codebase.
